# Multi-stage. The server binary is pure Go, so the runtime stage is
# distroless/static rather than distroless/base: DECISIONS.md records why the
# pure-Go SQLite driver was taken over the cgo one.

FROM golang:1.26.5-alpine AS build

WORKDIR /src

# Dependencies first, so a source-only change does not refetch the module
# graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
# main.version has to exist for this to do anything: `-X` naming a symbol
# nobody declared is discarded by the linker in silence.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/tdd ./cmd/tdd

# The data directory is made here so it can be copied in with an owner. The
# runtime stage is distroless and has no shell, so there is no mkdir there,
# and a /data owned by root is a container that cannot create its own database
# on a fresh volume.
RUN mkdir -p /data

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/tdd /usr/local/bin/tdd
COPY --from=build --chown=nonroot:nonroot /data /data

# One volume. The database, the content-addressed blobs, and config.toml all
# live under it. Docker seeds a fresh named volume from the image, so the
# ownership above is what the volume gets.
VOLUME ["/data"]

ENV TD_DB=/data/td.db \
    TD_ADDR=127.0.0.1:8080 \
    TD_TIMEZONE=America/Chicago

# TD_BASE_URL has no default on purpose: the server refuses to start without
# it rather than guessing its own public URL.

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/tdd"]
