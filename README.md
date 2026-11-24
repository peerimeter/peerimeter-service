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
intended for local development and single-process deployments. The single
`compose.yaml` includes the service and the digest-pinned
`postgres:18.7-alpine3.24` database. Set
`PEERIMETER_STORAGE_BACKEND=file` or `postgres` to select storage; set
`PEERIMETER_DATABASE_URL` to use remote PostgreSQL or Supabase. PostgreSQL
stores all four document types in JSONB and applies versioned Goose SQL
migrations on startup by default. Set `PEERIMETER_AUTO_MIGRATE=false` to disable
startup migrations, then run `peerimeter-service migrate` (or `make migrate` for
local development) to apply them explicitly. Start the HTTP service with the
`serve` command. Transactions protect network CIDR checks and concurrent
address allocation. PostgreSQL migration 002 enables Row Level Security on
the public service tables and Goose migration metadata table. It adds no
anonymous or per-user policies; roles subject to RLS have no rows allowed by
default, while the service's database-owner connection continues to operate.
MQTT coordination, user accounts, and multi-instance API authorization are
not included.

## Quick start with Docker Compose

The repository includes a Compose setup for the published multi-architecture
image. It binds the service to host loopback, so a trusted HTTPS reverse proxy
can forward requests to `127.0.0.1:8080`.

1. Clone this repository to the deployment host. The service container runs
   as UID/GID `1000`:

   ```sh
   git clone https://github.com/peerimeter/peerimeter-service.git
   cd peerimeter-service
   umask 077
   mkdir -p data
   sudo install -d -o 1000 -g 1000 data
   ```

   This ownership is for Docker without user namespace remapping. With rootless
   Docker or `userns-remap`, assign `data/` to the host UID and GID that map to
   container UID/GID `1000:1000`.

2. Create `.env` next to `compose.yaml`, with unique API/database credentials
   and a released service image tag:

   ```sh
   POSTGRES_PASSWORD="$(openssl rand -hex 32)"
   PEERIMETER_API_KEY="$(openssl rand -hex 32)"
   cat > .env <<EOF
   SERVICE_IMAGE_TAG=replace-with-a-release-tag
   PEERIMETER_STORAGE_BACKEND=postgres
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
   PostgreSQL storage requires a service release that includes PostgreSQL
   support. Earlier tags, including `v0.6.1`, predate PostgreSQL support; use
   the first PostgreSQL-capable release once published. Keep `.env` private and
   out of version control.

3. Start the service and bundled PostgreSQL database:

   ```sh
   docker compose pull
   docker compose up -d
   docker compose logs -f peerimeter-service
   ```

   Configure the reverse proxy to use `http://127.0.0.1:8080` as its upstream
   and terminate HTTPS there. `PEERIMETER_PUBLIC_API_URL` must be the public
   HTTPS URL agents will use.

### Choose a storage backend

- For file storage, set `PEERIMETER_STORAGE_BACKEND=file` in `.env`.
- For remote PostgreSQL or Supabase, set `PEERIMETER_STORAGE_BACKEND=postgres`
  and `PEERIMETER_DATABASE_URL` in `.env`, then run `docker compose up -d`.
  Use a direct connection or Supabase Session pooler. Transaction poolers do
  not keep the session Goose migrations need. Quote the whole database URL in
  `.env`, especially when it contains `&` query parameters.

The bundled PostgreSQL container starts in every Compose mode and stores its
data in a persistent `postgres-data` volume. It has no published host port.
Its connection URL is built from `POSTGRES_*`; use URI-safe credentials such
as the generated hexadecimal password, or percent-encode credentials in
`PEERIMETER_DATABASE_URL`. Goose migrations run when the service starts unless
`PEERIMETER_AUTO_MIGRATE=false` is set. To apply migrations manually in Compose,
run `docker compose run --rm peerimeter-service migrate`; the command uses the
database settings from `.env` and does not require the API key.

### Secure remote database connections

Use `sslmode=verify-full` for remote databases. It verifies the certificate
and server name. Publicly trusted certificates work without extra setup. To
trust a private CA without mounting a file, set `PEERIMETER_DATABASE_CA_CERT`
to its PEM certificate; it is added to the system trust store for this
database connection. `sslmode=disable` is only for local development hosts and
the bundled database.

`sslmode=require` encrypts traffic without verifying the server certificate.
The service rejects it by default. To explicitly allow it, set
`PEERIMETER_DATABASE_ALLOW_SSLMODE_REQUIRE=true`; this setting does not allow
remote `sslmode=disable` connections.

If the database uses a private CA that the container does not already trust,
mount the CA certificate read-only. The service needs it to verify the database;
a host file is not visible inside the container by default. Add this under the
existing `volumes:` list for `peerimeter-service` in `compose.yaml`, keeping the
other mounts such as `./data:/var/lib/peerimeter`. Do not replace the file with
this snippet. Replace the host path with the path to your CA file:

```yaml
      - /absolute/path/private-ca.pem:/run/peerimeter/private-ca.pem:ro
```

Set the URL in `.env` to use the mounted container path. Quote the whole value
so `&` stays inside the assignment:

```text
PEERIMETER_DATABASE_URL='postgres://USER:PERCENT_ENCODED_PASSWORD@HOST:5432/DB?sslmode=verify-full&sslrootcert=%2Frun%2Fpeerimeter%2Fprivate-ca.pem'
```

The container user (UID/GID `1000:1000`, or its mapped host user) must be able
to read the CA file; `:ro` prevents writes but does not grant read access.

### Back up your data

For file storage, back up the whole `data/` directory. It contains
`config.json`, and adjacent `nodes/` and `registrations/` directories. Stop all file-backed
writers before copying the directory, or use a filesystem-consistent snapshot
of the whole directory; the service does not coordinate multi-file backups.

The bundled PostgreSQL database can be backed up with `pg_dump`. This command
targets the database named by `POSTGRES_DB`; use it only when
`PEERIMETER_DATABASE_URL` is unset or selects that same database on the bundled
PostgreSQL instance. A matching database name on a remote server is not enough.
It writes to a new owner-only temporary file and replaces the previous backup
only after the dump succeeds:

```sh
backup_tmp="$(mktemp ./peerimeter.sql.XXXXXX)"
if docker compose exec -T postgres \
  sh -c 'pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB"' > "$backup_tmp"; then
  mv "$backup_tmp" peerimeter.sql
else
  rm -f "$backup_tmp"
  exit 1
fi
```

For a remote database or any other database, install the PostgreSQL client and
dump `PEERIMETER_DATABASE_URL` instead. This command uses a URL already set in
the shell, or reads it from `.env` as a fallback. It also keeps imported
environment values inside a subshell:

```sh
(
  backup_url=${PEERIMETER_DATABASE_URL:-}
  if [ -f .env ]; then
    set -a
    . ./.env
    set +a
  fi
  if [ -n "$backup_url" ]; then
    PEERIMETER_DATABASE_URL=$backup_url
  fi
  if [ -z "${PEERIMETER_DATABASE_URL:-}" ]; then
    echo "Set PEERIMETER_DATABASE_URL or add it to .env" >&2
    exit 1
  fi
  backup_tmp="$(mktemp ./peerimeter.sql.XXXXXX)"
  if pg_dump "$PEERIMETER_DATABASE_URL" > "$backup_tmp"; then
    mv "$backup_tmp" peerimeter.sql
  else
    rm -f "$backup_tmp"
    exit 1
  fi
)
```

For a private CA, the container URL uses the mounted path
`/run/peerimeter/private-ca.pem`. The container user (UID/GID `1000:1000`, or
its mapped host user) must be able to read the CA file; `:ro` prevents writes
but does not grant read access.

For a host-side `pg_dump`, use a certificate path readable on the host. Keep
the container URL in `.env`; run the host URL and dump together in a subshell
so it cannot affect later Compose commands:

```sh
(
  export PEERIMETER_DATABASE_URL='postgres://USER:PERCENT_ENCODED_PASSWORD@HOST:5432/DB?sslmode=verify-full&sslrootcert=%2Fabsolute%2Fpath%2Fprivate-ca.pem'
  backup_tmp="$(mktemp ./peerimeter.sql.XXXXXX)"
  if pg_dump "$PEERIMETER_DATABASE_URL" > "$backup_tmp"; then
    mv "$backup_tmp" peerimeter.sql
  else
    rm -f "$backup_tmp"
    exit 1
  fi
)
```

Use the mounted container path in `.env` for the service. The host-side
certificate path is not available inside the container.

### Storage backend changes

The service no longer imports file-backed data into PostgreSQL. Keep using the
file backend for existing current-format file stores. Before upgrading, export
and convert any node documents embedded in `config.json` into
`nodes/<network>/<node>.yaml`, and move records from the old
`config.json.registrations/` directory into `registrations/`; back up the index
and all sidecar directories first. The new file backend does not read those old
locations, and the next index write drops embedded node documents. Before
switching to PostgreSQL, use an operator-managed export/import process.


## API

All routes except `GET /healthz` and `GET /health` require
`Authorization: Bearer <credential>`. Management calls use
`PEERIMETER_API_KEY`; agent join bundles contain a network-scoped enrollment
token. Requests and responses use JSON. Config and registration documents use
`"version": 1` and request bodies are limited to 1 MiB.

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | Unauthenticated liveness check (`200` JSON `{"success":true}`); does not check dependencies. |
| `GET` | `/health` | Unauthenticated readiness check (`200` JSON `{"success":true}` when file storage is configured or PostgreSQL is reachable; `503` JSON when PostgreSQL cannot be reached; the database error is logged by the service). |
| `GET` | `/api/v1/version` | Return the running service release version. |
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
managed network. Agent registrations may include `agent_cli_version`; the
service preserves it in registered peer documents for dashboard display.

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

The `.envrc` loads the pinned Devbox environment and then loads `.env` when it
exists. Devbox provides PostgreSQL `18.6` for local development. Initialize and
start its database once, then create the integration-test database:

```sh
devbox run postgres-init
devbox run postgres-up
```

In another shell inside the project, create the test database once and run the
integration suite over the local PostgreSQL socket:

```sh
devbox run postgres-create-test-db
PEERIMETER_TEST_DATABASE_URL="postgresql://$(id -un)@/peerimeter_test?host=$PGHOST&sslmode=disable" make test-integration
```

Stop PostgreSQL with `devbox run postgres-down`. Its data stays in the ignored
`.devbox/virtenv/postgresql/data` directory. The socket uses a short path under
`$TMPDIR` for compatibility with Unix socket path limits.

The local `.env.example` values are for development only. `.env` is ignored by
Git. `make run` starts the `serve` command and binds to `127.0.0.1` by default;
the container overrides this with `0.0.0.0` inside its isolated network
namespace. Run `make migrate` to apply pending database migrations manually.

`make test` runs unit tests. PostgreSQL integration tests use a dedicated
database because they apply migrations and write records:

```sh
PEERIMETER_TEST_DATABASE_URL='postgresql://user:password@host:5432/test?sslmode=verify-full' make test-integration
```

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `PEERIMETER_API_KEY` | Required | Shared management credential. Use a high-entropy secret. |
| `PEERIMETER_STORAGE_BACKEND` | `file` | Persistence selection: `file` or `postgres`. |
| `PEERIMETER_DATABASE_URL` | Compose builds a URL from the `POSTGRES_*` variables | PostgreSQL connection URL; required when the service backend is `postgres`. Remote connections require `sslmode=verify-full`, unless `PEERIMETER_DATABASE_ALLOW_SSLMODE_REQUIRE=true`; local development can use `sslmode=disable`. |
| `PEERIMETER_DATABASE_CA_CERT` | Empty | Optional PEM CA certificate added to the system trust store for PostgreSQL TLS verification. |
| `PEERIMETER_DATABASE_ALLOW_SSLMODE_REQUIRE` | `false` | Explicitly allow `sslmode=require`, which encrypts the connection but does not verify the server certificate. Does not allow remote `sslmode=disable`. |
| `PEERIMETER_DATA_FILE` | `/var/lib/peerimeter/config.json` | File-backend path. Sibling `nodes/` and `registrations/` directories are included. |
| `POSTGRES_USER` | `peerimeter` in Compose | User for the bundled PostgreSQL service. |
| `POSTGRES_PASSWORD` | Local-only Compose fallback | Set a unique high-entropy password for deployment. |
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
a release. While the service remains in `0.y.z`, breaking changes use the next
minor version; their GitHub release description starts with **Breaking changes
included** and directs readers to the linked pull requests for compatibility
details and upgrade instructions.
