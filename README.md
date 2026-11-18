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
migrations on startup. Transactions protect network CIDR checks and concurrent
address allocation.
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

The PostgreSQL service stores data in a persistent `postgres-data` volume.
Back up the database without replacing a previous backup if the dump fails:

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

`mktemp` creates a unique owner-only file in the destination directory. The
command above backs up the database named by `POSTGRES_DB` in the bundled
container. Use it only when `PEERIMETER_DATABASE_URL` is unset or points to
that same database. For a remote URL or any other database, install the
PostgreSQL client and dump the URL itself to a unique temporary file before
replacing the previous backup. This subshell loads `.env` only as a fallback,
preserves a URL already set in the shell, and keeps import-related settings
out of the deployment shell:

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

For a private CA, `sslrootcert` in a host-side backup URL must point to a
certificate path readable by the host `pg_dump`; a path mounted only inside
the service container (such as `/run/peerimeter/private-ca.pem`) is not
available on the host. Keep `sslmode=verify-full`. If you previously sourced
`.env` into the deployment shell using the older snippet, run
`unset PEERIMETER_IMPORT_FILE PEERIMETER_IMPORT_SIDECAR_ONLY` before recreating
the service container after the import is complete.

If you choose the file backend, back up the whole `data/` directory. It
contains `config.json` and adjacent `nodes/` and `registrations/` directories.

Set `PEERIMETER_STORAGE_BACKEND=file` in `.env` to use file storage. For a
remote PostgreSQL or Supabase database, set `PEERIMETER_STORAGE_BACKEND=postgres`
and `PEERIMETER_DATABASE_URL` in `.env` with `sslmode=verify-full` (quote the
URL value so a shell can read it for backups), then run:
`docker compose up -d`. Use a direct connection or Supabase Session pooler URL;
transaction pooler URLs do not preserve the session needed by Goose migrations.

Remote connections without certificate and hostname verification are rejected.
`sslmode=disable` is accepted only for local development hosts (`localhost`,
loopback addresses, or the bundled Compose `postgres` service). The bundled
database container starts in every Compose mode so one file covers file, bundled
PostgreSQL, and remote PostgreSQL selections. Its URL is built from
`POSTGRES_*`; use URI-safe credentials such as the generated hexadecimal
password, or provide `PEERIMETER_DATABASE_URL` with percent-encoded credentials.
The service runs embedded Goose migrations at startup.

Publicly trusted database certificates need no extra mount. For a private CA,
add this read-only bind mount under `peerimeter-service.volumes` in
`compose.yaml`, then set `sslrootcert` to that in-container path in the URL:

```yaml
      - /absolute/path/private-ca.pem:/run/peerimeter/private-ca.pem:ro
```

Append `&sslrootcert=%2Frun%2Fpeerimeter%2Fprivate-ca.pem` to
`PEERIMETER_DATABASE_URL` (or use `?sslrootcert=...` if it has no query string).

To import an existing file store, stop its writer and copy `config.json`, if
present, plus the sibling `nodes/` and `registrations/` directories and the
legacy `config.json.registrations/` directory when present into the Compose
`data/` mount. For the source setup in the hosted guide, the default is
`$HOME/.local/share/peerimeter`; copy that directory’s contents, including
`config.json`, `nodes/`, `registrations/`, and `config.json.registrations/`
when present. If the store is
already in `data/`, skip the copy. Make the copied files readable by container
UID/GID `1000:1000` (or the mapped host IDs for rootless/remapped Docker). Then
set `PEERIMETER_IMPORT_FILE` to its
`config.json` path on a one-time startup. With the default Compose mount, use
`/var/lib/peerimeter/config.json`; host relative paths are not visible in the
container. Stop every file-backed service or process that can write to the
store before starting the import; the importer reads a snapshot and does not
coordinate with file-backed writers. The index file must exist and be a
regular file, except for sidecar-only stores with no `config.json`; for those,
also set `PEERIMETER_IMPORT_SIDECAR_ONLY=true`. This flag is accepted only when
sibling node or registration files exist, preventing a typo in the index path
from silently importing incomplete data. Startup warns if a file store is
present but no import path is configured. Import copies the network metadata,
network config, node configs, and registrations in one transaction, and refuses
to run unless the PostgreSQL tables are empty. Keep the original files until
you have verified the imported data and backed up the database. Remove
`PEERIMETER_IMPORT_FILE` and `PEERIMETER_IMPORT_SIDECAR_ONLY` after a successful
import, then run
`docker compose up -d peerimeter-service` to recreate the container with the
updated environment. A plain `docker compose restart` does not apply `.env` changes.

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
| `PEERIMETER_STORAGE_BACKEND` | `file` | Persistence selection: `file` or `postgres`. |
| `PEERIMETER_DATABASE_URL` | Compose builds a URL from the `POSTGRES_*` variables | PostgreSQL connection URL; required when the service backend is `postgres`. Remote connections require `sslmode=verify-full`; local development can use `sslmode=disable`. |
| `PEERIMETER_IMPORT_FILE` | Empty | Optional one-time import source path; Compose uses `/var/lib/peerimeter/config.json`. Import requires an empty PostgreSQL database and includes sibling `nodes/` and `registrations/` data. |
| `PEERIMETER_IMPORT_SIDECAR_ONLY` | `false` | Explicitly allow import when the `config.json` index is absent but node or registration sidecars exist. |
| `PEERIMETER_DATA_FILE` | `/var/lib/peerimeter/config.json` | File-backend path and optional PostgreSQL import source. Sibling `nodes/` and `registrations/` directories are included. |
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
a release.
