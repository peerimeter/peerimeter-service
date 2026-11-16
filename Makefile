GO ?= go

.PHONY: env test build run fmt

env:
	@test -f .env || cp .env.example .env

test:
	CGO_ENABLED=0 $(GO) test ./...

build:
	CGO_ENABLED=0 $(GO) build ./...

run:
	$(GO) run ./cmd/peerimeter-service

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')
