FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN mkdir -p /out /var/lib/peerimeter && chown 65532:65532 /var/lib/peerimeter
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/peerimeter-service ./cmd/peerimeter-service

FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=build /out/peerimeter-service /peerimeter-service
COPY --from=build --chown=65532:65532 /var/lib/peerimeter /var/lib/peerimeter
USER 65532:65532
EXPOSE 8080
VOLUME ["/var/lib/peerimeter"]
ENTRYPOINT ["/peerimeter-service"]
