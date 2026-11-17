# Peerimeter Service

Peerimeter Service is the control-plane HTTP API used by Peerimeter agents to
store and exchange network and node configuration. This repository owns the
versioned HTTP/JSON contract; the [agent](https://github.com/peerimeter/peerimeter-agent)
and [dashboard](https://github.com/peerimeter/peerimeter-dashboard) are separate
projects. The hosted setup guide is at <https://service.peerimeter.app/>.

## Current status

The service stores network metadata and configuration in a local JSON file,
with node configuration and registrations in adjacent directories. Writes are
serialized and replaced atomically within one process. Use one service instance
and one data file per directory because node and registration directories are
shared siblings. PostgreSQL, MQTT coordination, user accounts, and multi-instance
locking are not included.

## Quick start with Docker Compose

The repository includes a Compose setup for the published multi-architecture
image. It binds the service to host loopback, so a trusted HTTPS reverse proxy
can forward requests to `127.0.0.1:8080`.

1. Clone this repository to the deployment host, then prepare persistent
   storage. This provides `compose.yaml` in the deployment directory. The
   container runs as UID/GID `1000`:

   ```sh
   git clone https://github.com/peerimeter/peerimeter-service.git
   cd peerimeter-service
   mkdir -p data
   sudo install -d -o 1000 -g 1000 data
   umask 077
   ```

2. Create `.env` next to `compose.yaml`, using a released image tag and a unique
   API key:

   ```sh
   cat > .env <<'EOF'
   SERVICE_IMAGE_TAG=replace-with-a-release-tag
   PEERIMETER_API_KEY=replace-with-a-unique-random-secret
   PEERIMETER_PUBLIC_API_URL=https://service.example.com
   EOF
   chmod 600 .env
   ```

   Generate the key with `openssl rand -hex 32`. Replace the example image tag
   with a version published at
   [GitHub Releases](https://github.com/peerimeter/peerimeter-service/releases).
   Keep `.env` private and out of version control.

3. Start the service:

   ```sh
   docker compose pull
   docker compose up -d
   docker compose logs -f peerimeter-service
   ```

   Configure the reverse proxy to use `http://127.0.0.1:8080` as its upstream
   and terminate HTTPS there. `PEERIMETER_PUBLIC_API_URL` must be the public
   HTTPS URL agents will use.

Back up the whole `data/` directory. It contains `config.json` and the adjacent
`nodes/` and `registrations/` directories. These hold node
documents and registration state.

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

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `PEERIMETER_API_KEY` | Required | Shared management credential. Use a high-entropy secret. |
| `PEERIMETER_DATA_FILE` | `/var/lib/peerimeter/config.json` | Main data file; use one data file per directory because sibling `nodes/` and `registrations/` directories are shared. |
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
