# Peerimeter Service

The Peerimeter control-plane API. This repository is independent from
[`peerimeter-agent`](https://github.com/peerimeter/peerimeter-agent) and owns the
versioned HTTP/JSON contract consumed by agents. See the [setup guide](https://service.peerimeter.app/)
for running the service from source or with Docker Compose. The documentation
is also published from this repository’s `docs/` directory through GitHub Pages.

## Status

Phase 3 service work is in progress. The initial service stores network and node
configuration in one local JSON file. It uses an in-memory write queue and
atomic file replacement for concurrent requests on one process. PostgreSQL,
MQTT, user authentication and multi-instance coordination are not part of this
initial implementation.

## Development

Requires the Go version in `.go-version`; `go.mod` declares the compatible Go 1.27 language line.

```sh
make env
make test
make run
```

`make env` copies `.env.example` to `.env` if `.env` does not already exist. The
`.envrc` loads `.env` when using [direnv](https://direnv.net/); approve it once
with `direnv allow` and run the commands with direnv active. `.env` is ignored
by Git. Edit it for local development;
the example API key is not suitable for deployment. The `.env` defaults include
`CGO_ENABLED=0` and `PORT=8080`. `make run` requires direnv to load the local
API key, or `PEERIMETER_API_KEY` to be exported in the shell. The service binds
to `127.0.0.1` by default for local development; the container image overrides
this to `0.0.0.0`. `PEERIMETER_API_KEY` must be set to a high entropy secret
outside local development. `PEERIMETER_DATA_FILE` defaults to
`/var/lib/peerimeter/config.json` in the service; without direnv, set it to a
writable path if that location is not writable by your local process. The
`PEERIMETER_PUBLIC_API_URL` is required for join-bundle creation and must be
HTTPS.
`PEERIMETER_CORS_ALLOWED_ORIGINS` optionally enables browser access from a
comma-separated allowlist of exact HTTP(S) origins, such as
`https://dashboard.example.com,https://admin.example.com`. Values may include a
port, but not a path, credentials, query, fragment, or wildcard. The service
returns CORS headers only for listed origins and permits the API's `GET`,
`POST`, and `PUT` requests with `Authorization` and `Content-Type` headers. Leave
it empty to require the dashboard and API to share an origin.
The network and node `PUT` bodies must be JSON objects with `"version": 1`.

All API routes except `/healthz` require `Authorization: Bearer <key>`. The
`GET /api/v1/join-bundle?network_id={networkID}` response contains a
Base64URL-encoded v1 JSON bundle with that network ID; the `network_id` must
refer to an existing managed network. Its `enrollment_token` is the same static
API key. Anyone holding it can access all networks and nodes. This shared
credential has no per-agent scope or expiry.
Terminate TLS at a trusted reverse proxy before exposing the API beyond a
trusted local development environment.

Use `GET`/`PUT /api/v1/networks/{networkID}/config` and
`GET`/`PUT /api/v1/networks/{networkID}/nodes/{nodeID}/config` to pull or push
config. Files are replaced atomically with mode `0600`. Writes are serialized
inside one process; run a single service instance against each data file.
Create a managed network with `POST /api/v1/networks` using a version 1 object
with `id`, `name`, and a canonical private IPv4 WireGuard `cidr` (for example,
`10.90.0.0/24`). The service also exposes `GET /api/v1/networks` and
`GET /api/v1/networks/{networkID}` for listing and reading network metadata.
Network metadata is separate from the legacy network config exchange
`/config` endpoints.
User-managed node config documents are stored as individual YAML files under
`<PEERIMETER_DATA_FILE>.nodes/{networkID}/{nodeID}.yaml`; agent registrations
are stored separately under the sibling `.registrations/` directory so a
config upload cannot change a node's assigned address. Both directories must
be backed up with the main data file. The REST API continues to accept and
return version 1 JSON. Existing node config documents in the earlier single
JSON file are migrated to this layout at startup. An authenticated
`GET /api/v1/networks/{networkID}/nodes/{nodeID}/peers` returns the other stored
node documents as `{ "version": 1, "peers": { "node-id": { ... } } }`. This
endpoint is only a document exchange primitive: it does not enroll or
authenticate individual devices, discover endpoints, or establish tunnels.
The authenticated dashboard can request
`GET /api/v1/networks/{networkID}/peers` to list all registered node documents
in that network without a node-specific self ID.
`PUT /api/v1/networks/{networkID}/nodes/{nodeID}/registration` accepts a v1
node report (`public_key`, `listen_port`, `primary_interface`,
`local_addresses`, and optional `public_endpoint`). For a network created with
the API, the service atomically allocates an unused tunnel IPv4 address from
its CIDR, returns the node document, and writes it to that node's YAML file.
Re-registering the same node ID and public key preserves its address; using a
different key with that ID returns a conflict. The current shared API key still
authorizes every network and node operation; registration does not yet issue
per-device credentials.

Pull requests build the image but do not publish it. On `main`, Conventional
Commit messages determine whether a release is due: `feat` increments minor,
`fix` and related patch types increment patch, and `BREAKING CHANGE` increments
major. A release run publishes both `vX.Y.Z` and `X.Y.Z` tags for
`linux/amd64` and `linux/arm64` at
`ghcr.io/peerimeter/peerimeter-service`. The first main run seeds `v0.0.0` as
the history baseline; commits without a version bump do not publish an image.
