# Peerimeter Service

Peerimeter Service is the control-plane HTTP API used by Peerimeter agents to
store and exchange network and node configuration. This repository owns the
versioned HTTP/JSON contract; the [agent](https://github.com/peerimeter/peerimeter-agent)
and [dashboard](https://github.com/peerimeter/peerimeter-dashboard) are separate
projects. The hosted setup guide is at <https://service.peerimeter.app/>.

## Current status

The service supports two selectable persistence backends. The service's
environment default is `file`, which stores network metadata/configuration in
JSON and node configuration and registrations in adjacent directories. It is
intended for local development and single-process deployments. The base Compose
file uses the file backend; `compose.postgres.yaml` adds a PostgreSQL 18 Alpine
database and selects the `postgres` backend. A separate overlay supports a
remote PostgreSQL or Supabase database. PostgreSQL stores all four document
types in JSONB and applies versioned Goose SQL migrations on startup.
Transactions protect network CIDR checks and concurrent address allocation.
MQTT coordination, user accounts, and multi-instance API authorization are
not included.

## Quick start with Docker Compose

The repository includes a Compose setup for the published multi-architecture
image. It binds the service to host loopback, so a trusted HTTPS reverse proxy
can forward requests to `127.0.0.1:8080`.

1. Clone this repository to the deployment host. This provides `compose.yaml`
   in the deployment directory. Create the optional file-backend/import
   directory if needed. The service container runs as UID/GID `1000`:

   ```sh
   git clone https://github.com/peerimeter/peerimeter-service.git
   cd peerimeter-service
   umask 077
   mkdir -p data
   sudo install -d -o 1000 -g 1000 data
   ```

2. Create `.env` next to `compose.yaml`, with unique API/database credentials
   and a released service image tag:

   ```sh
   POSTGRES_PASSWORD="$(openssl rand -hex 32)"
   PEERIMETER_API_KEY="$(openssl rand -hex 32)"
   cat > .env <<EOF
   SERVICE_IMAGE_TAG=replace-with-a-release-tag
   PEERIMETER_PUBLIC_API_URL=https://service.example.com
   POSTGRES_USER=peerimeter
   POSTGRES_PASSWORD=$POSTGRES_PASSWORD
   POSTGRES_DB=peerimeter
   PEERIMETER_API_KEY=$PEERIMETER_API_KEY
   EOF
   unset POSTGRES_PASSWORD PEERIMETER_API_KEY
   chmod 600 .env
   ```

   Generate the key with `openssl rand -hex 32`. Replace the example image tag
   with a version published at
   [GitHub Releases](https://github.com/peerimeter/peerimeter-service/releases).
   Keep `.env` private and out of version control.

3. Start the service and bundled PostgreSQL database:

   ```sh
   docker compose -f compose.yaml -f compose.postgres.yaml pull
   docker compose -f compose.yaml -f compose.postgres.yaml up -d
   docker compose -f compose.yaml -f compose.postgres.yaml logs -f peerimeter-service
   ```

   Configure the reverse proxy to use `http://127.0.0.1:8080` as its upstream
   and terminate HTTPS there. `PEERIMETER_PUBLIC_API_URL` must be the public
   HTTPS URL agents will use.

The PostgreSQL overlay stores data in a persistent `postgres-data` volume.
Back up the database without replacing a previous backup if the dump fails:

```sh
docker compose -f compose.yaml -f compose.postgres.yaml exec -T postgres \
  sh -c 'pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB"' > peerimeter.sql.tmp \
  && mv peerimeter.sql.tmp peerimeter.sql
```

If you choose the file backend, back up the whole `data/` directory. It
contains `config.json` and adjacent `nodes/` and `registrations/` directories.

For file storage, run `docker compose -f compose.yaml up -d`. For a remote
PostgreSQL or Supabase database, set `PEERIMETER_DATABASE_URL` in `.env` with
`sslmode=verify-full` and run:

```sh
docker compose -f compose.yaml -f compose.remote-postgres.yaml up -d
```

Remote connections without certificate and hostname verification are rejected.
`sslmode=disable` is accepted only for local development hosts (`localhost`,
loopback addresses, or the bundled Compose `postgres` service). The bundled
database URL is built from `POSTGRES_*`; use URI-safe credentials such as the
generated hexadecimal password, or provide `PEERIMETER_DATABASE_URL` with
percent-encoded credentials.
The service runs embedded Goose migrations at startup.
To import an existing file store, set `PEERIMETER_IMPORT_FILE` to its
`config.json` path on a one-time startup. With the default Compose mount, use
`/var/lib/peerimeter/config.json`; host relative paths are not visible in the
container. Stop every file-backed service or process that can write to the
store before starting the import; the importer reads a snapshot and does not
coordinate with file-backed writers. The index file must exist and be a
regular file. Startup warns if a file store is present but no import path is
configured. Import copies the network metadata,
network config, node configs, and registrations in one transaction, and refuses
to run unless the PostgreSQL tables are empty. Keep the original files until
you have verified the imported data and backed up the database. Remove
`PEERIMETER_IMPORT_FILE` after a successful import.

## API

All routes except `GET /healthz` require
`Authorization: Bearer <credential>`. Management calls use
`PEERIMETER_API_KEY`; agent join bundles contain a network-scoped enrollment
token. Requests and responses use JSON. Config and registration documents use
`"version": 1` and request bodies are limited to 1 MiB.

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | Unauthenticated health check (`204` when healthy). |
| `GET` | `/api/v1/networks` | List managed network metadata. |
| `POST` | `/api/v1/networks` | Create a managed network. |
| `GET` | `/api/v1/networks/{networkID}` | Read network metadata. |
| `GET` / `PUT` | `/api/v1/networks/{networkID}/config` | Read or replace network config. |
| `GET` | `/api/v1/networks/{networkID}/peers` | List registered node documents for the dashboard. |
| `GET` / `PUT` | `/api/v1/networks/{networkID}/nodes/{nodeID}/config` | Read or replace a node config document. |
| `GET` | `/api/v1/networks/{networkID}/nodes/{nodeID}/peers` | List other registered node documents, excluding `{nodeID}`. |
| `PUT` | `/api/v1/networks/{networkID}/nodes/{nodeID}/registration` | Register a node and allocate its tunnel address. |
| `GET` | `/api/v1/join-bundle?network_id={networkID}` | Create a Base64URL-encoded agent join bundle. |

`PUT` config requests accept any JSON object with `"version": 1`; the service
stores and returns that document. Creating a network requires a canonical
private IPv4 CIDR from `/8` through `/30`. The CIDR must not overlap another
managed network.

### Usage examples

Set the base URL and management key in your shell. Use the public HTTPS URL
after deploying behind the reverse proxy:

```sh
export API_URL=https://service.example.com
export PEERIMETER_API_KEY='your-management-key'
```

Create a network and list networks:

```sh
curl --fail-with-body -X POST "$API_URL/api/v1/networks" \
  -H "Authorization: Bearer $PEERIMETER_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"version":1,"id":"home","name":"Home","cidr":"10.90.0.0/24"}'

curl --fail-with-body "$API_URL/api/v1/networks" \
  -H "Authorization: Bearer $PEERIMETER_API_KEY"
```

Request a join bundle for an existing network. The response's `token` value is
itself Base64URL-encoded; pass it to the agent's join flow. Treat it as a
credential:

```sh
curl --fail-with-body --get "$API_URL/api/v1/join-bundle" \
  -H "Authorization: Bearer $PEERIMETER_API_KEY" \
  --data-urlencode 'network_id=home'
```

Read or replace configuration. Replace the example documents with the schema
your agent or dashboard uses:

```sh
curl --fail-with-body "$API_URL/api/v1/networks/home/config" \
  -H "Authorization: Bearer $PEERIMETER_API_KEY"

curl --fail-with-body -X PUT "$API_URL/api/v1/networks/home/config" \
  -H "Authorization: Bearer $PEERIMETER_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"version":1,"name":"home-config"}'
```

For an agent, the enrollment token in the join bundle authorizes requests only
for its network. Registration accepts a v1 report containing a WireGuard
`public_key`, `listen_port`, `primary_interface`, and `local_addresses`; it
returns the node document with its allocated `tunnel_address`. The agent
performs this registration flow; see the
[agent README](https://github.com/peerimeter/peerimeter-agent#readme) for its
commands. The peers endpoints exchange stored documents only: they do not
discover reachable endpoints or establish tunnels.

## Run from source

Use the Go version pinned in `.go-version` (`go.mod` declares the Go 1.27
language line). With [direnv](https://direnv.net/) installed:

```sh
make env                 # create local .env from .env.example if missing
direnv allow             # once per checkout
make test
make run
```

The local `.env.example` values are for development only. `.env` is ignored by
Git. `make run` binds to `127.0.0.1` by default; the container overrides this
with `0.0.0.0` inside its isolated network namespace.

`make test` runs unit tests. PostgreSQL integration tests use a dedicated
database because they apply migrations and write records:

```sh
PEERIMETER_TEST_DATABASE_URL='postgresql://user:password@host:5432/test?sslmode=verify-full' make test-integration
```

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `PEERIMETER_API_KEY` | Required | Shared management credential. Use a high-entropy secret. |
| `PEERIMETER_STORAGE_BACKEND` | `file` | Persistence selection: `file` or `postgres`; the PostgreSQL Compose overlay selects `postgres`. |
| `PEERIMETER_DATABASE_URL` | Compose builds a URL from the `POSTGRES_*` variables | PostgreSQL connection URL; required when the service backend is `postgres`. Remote connections require `sslmode=verify-full`; local development can use `sslmode=disable`. |
| `PEERIMETER_IMPORT_FILE` | Empty | Optional one-time import source `config.json`; import requires an empty PostgreSQL database and includes sibling `nodes/` and `registrations/` data. |
| `PEERIMETER_DATA_FILE` | `/var/lib/peerimeter/config.json` | File-backend path and optional PostgreSQL import source. Sibling `nodes/` and `registrations/` directories are included. |
| `POSTGRES_USER` | `peerimeter` in Compose | User for the bundled PostgreSQL service. |
| `POSTGRES_PASSWORD` | Required by Compose | Unique high-entropy password for the bundled PostgreSQL service. |
| `POSTGRES_DB` | `peerimeter` in Compose | Database created for the bundled PostgreSQL service. |
| `PEERIMETER_LISTEN_ADDR` | `127.0.0.1` | Bind address. Set to `0.0.0.0` in a container. |
| `PORT` | `8080` | HTTP port. |
| `PEERIMETER_PUBLIC_API_URL` | Empty | Public API URL included in join bundles; required for bundle creation. |
| `PEERIMETER_REQUIRE_HTTPS` | `true` | Require HTTPS for the public URL in join bundles. Disable only for local or trusted private use. |
| `PEERIMETER_CORS_ALLOWED_ORIGINS` | Empty | Optional comma-separated exact HTTP(S) origins. Wildcards and paths are rejected. |

Use a trusted reverse proxy for TLS before exposing the service beyond a
trusted local development environment. The service uses one shared static
management key; enrollment tokens are derived from it and scoped to one network.
Rotating the key invalidates existing join bundles and enrollment tokens, so
issue fresh bundles to agents that should remain connected.

## Container image and releases

The service image is published as
`ghcr.io/peerimeter/peerimeter-service` for `linux/amd64` and `linux/arm64`.
Pull requests build but do not publish images. Main branch releases publish
versioned tags (`vX.Y.Z` and `X.Y.Z`) when Conventional Commit history requires
a release.
