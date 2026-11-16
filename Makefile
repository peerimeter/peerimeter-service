GO ?= go

.PHONY: env test build run fmt

env:
	@test -f .env || cp .env.example .env

test:
	$(GO) test ./...

build:
	$(GO) build ./...

run:
	$(GO) run ./cmd/peerimeter-service

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')
