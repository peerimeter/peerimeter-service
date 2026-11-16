# Peerimeter Service

The Peerimeter control-plane API. This repository is independent from
[`peerimeter-agent`](https://github.com/peerimeter/peerimeter-agent) and owns the
versioned HTTP/JSON contract consumed by agents.

## Status

Phase 3 service work is in progress. The initial service stores network and node
configuration in one local JSON file. It uses an in-memory write queue and
atomic file replacement for concurrent requests on one process. PostgreSQL,
MQTT, user authentication and multi-instance coordination are not part of this
initial implementation.

## Development

Requires the Go 1.27 toolchain line declared in `go.mod`.

```sh
go test ./...
PEERIMETER_API_KEY=local-development-only \
PEERIMETER_DATA_FILE=./data/config.json \
go run ./cmd/peerimeter-service
```

Set `PEERIMETER_API_KEY` to a high entropy secret and optionally set
`PEERIMETER_DATA_FILE` (default `/var/lib/peerimeter/config.json`) and
`PEERIMETER_PUBLIC_API_URL` (required for join-bundle creation; must be HTTPS).
The network and node `PUT` bodies must be JSON objects with `"version": 1`.

All API routes except `/healthz` require `Authorization: Bearer <key>`. The
`GET /api/v1/join-bundle` response contains a Base64URL-encoded v1 JSON bundle;
its `enrollment_token` is the same static API key. Anyone holding it can access
all networks and nodes. This shared credential has no per-agent scope or expiry.
Terminate TLS at a trusted reverse proxy before exposing the API beyond a
trusted local development environment.

Use `GET`/`PUT /api/v1/networks/{networkID}/config` and
`GET`/`PUT /api/v1/networks/{networkID}/nodes/{nodeID}/config` to pull or push
config. Files are replaced atomically with mode `0600`. Writes are serialized
inside one process; run a single service instance against each data file.

Version tags (`v*`) publish `linux/amd64` and `linux/arm64` images to
`ghcr.io/peerimeter/peerimeter-service`. Pull requests build the image but do
not publish it.
