GO ?= go

.PHONY: env test test-integration build run fmt

env:
	@test -f .env || cp .env.example .env

test:
	$(GO) test ./...

test-integration:
	@test -n "$$PEERIMETER_TEST_DATABASE_URL" || (echo 'Set PEERIMETER_TEST_DATABASE_URL to a dedicated PostgreSQL test database' >&2; exit 1)
	PEERIMETER_TEST_DATABASE_URL="$$PEERIMETER_TEST_DATABASE_URL" $(GO) test ./internal/postgresstore -run '^TestPostgres' -count=1

build:
	$(GO) build ./...

run:
	$(GO) run ./cmd/peerimeter-service

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')
