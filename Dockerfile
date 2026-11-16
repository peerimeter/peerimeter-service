FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN mkdir -p /out /var/lib/peerimeter && chown 1000:1000 /var/lib/peerimeter
RUN printf 'app:x:1000:1000:Peerimeter Service:/var/lib/peerimeter:/sbin/nologin\n' > /out/passwd \
    && printf 'app:x:1000:\n' > /out/group
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/peerimeter-service ./cmd/peerimeter-service

FROM scratch

COPY --from=build /out/peerimeter-service /peerimeter-service
COPY --from=build /out/passwd /etc/passwd
COPY --from=build /out/group /etc/group
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=1000:1000 /var/lib/peerimeter /var/lib/peerimeter
USER app
EXPOSE 8080
VOLUME ["/var/lib/peerimeter"]
ENTRYPOINT ["/peerimeter-service"]
