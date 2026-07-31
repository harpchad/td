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
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/tdd ./cmd/tdd

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/tdd /usr/local/bin/tdd

# One volume. The database, the content-addressed blobs, and config.toml all
# live under it.
VOLUME ["/data"]

ENV TD_DB=/data/td.db \
    TD_ADDR=127.0.0.1:8080 \
    TD_TIMEZONE=America/Chicago

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/tdd"]
