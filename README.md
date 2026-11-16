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

Requires the Go version in `.go-version`; `go.mod` declares the compatible Go 1.27 language line.

```sh
make env
make test
make run
```

`make env` copies `.env.example` to `.env` if `.env` does not already exist. The
`.envrc` loads `.env` when using [direnv](https://direnv.net/); approve it once
with `direnv allow`. `.env` is ignored by Git. Edit it for local development;
the example API key is not suitable for deployment. The `.env` defaults include
`CGO_ENABLED=0` and `PORT=8080`. `PEERIMETER_API_KEY` must be set to a high
entropy secret outside local development. `PEERIMETER_DATA_FILE` defaults to
`/var/lib/peerimeter/config.json` in the service, and
`PEERIMETER_PUBLIC_API_URL` is required for join-bundle creation and must be
HTTPS.
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

Pull requests build the image but do not publish it. On `main`, Conventional
Commit messages determine whether a release is due: `feat` increments minor,
`fix` and related patch types increment patch, and `BREAKING CHANGE` increments
major. A release run publishes both `vX.Y.Z` and `X.Y.Z` tags for
`linux/amd64` and `linux/arm64` at
`ghcr.io/peerimeter/peerimeter-service`. The first main run seeds `v0.0.0` as
the history baseline; commits without a version bump do not publish an image.
