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

Requires Go 1.27.4.

```sh
export GOTOOLCHAIN=go1.27.4
go test ./...
go run ./cmd/peerimeter-service
```

See the API and deployment documentation as those features land. The service
requires `PEERIMETER_API_KEY` and a writable JSON data path; never commit real
credentials or configuration data.
