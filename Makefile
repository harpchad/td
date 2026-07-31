SHELL := /bin/bash
.DEFAULT_GOAL := check

# Pinned tool versions. Never a branch, never latest.
GOFUMPT_VERSION      := v0.9.1
GOLANGCI_LINT_VERSION := v2.6.1
GOVULNCHECK_VERSION  := v1.1.4

GOBIN ?= $(shell go env GOPATH)/bin
BUILD_DIR := build

SEED := testdata/seed.json
DEV_DB := $(BUILD_DIR)/dev.db

.PHONY: check
## check is the one definition of passing. CI runs exactly this.
check: fmt lint test build vuln boundary schema
	@echo "check: ok"

.PHONY: fmt
fmt:
	@echo "==> gofumpt"
	@out=$$($(GOBIN)/gofumpt -l .); \
	if [ -n "$$out" ]; then echo "not gofumpt-clean:"; echo "$$out"; exit 1; fi

.PHONY: lint
lint:
	@echo "==> golangci-lint"
	@golangci-lint run

.PHONY: test
test:
	@echo "==> go test"
	@go test ./...

.PHONY: build
## The client cross-compiles with no cgo. The server builds for the container
## target it actually ships to.
build:
	@echo "==> build"
	@mkdir -p $(BUILD_DIR)
	@CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o $(BUILD_DIR)/td-darwin-arm64 ./cmd/td
	@CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o $(BUILD_DIR)/td-darwin-amd64 ./cmd/td
	@CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -o $(BUILD_DIR)/td-linux-amd64   ./cmd/td
	@CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -o $(BUILD_DIR)/tdd-linux-amd64  ./cmd/tdd
	@CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o $(BUILD_DIR)/tdd-darwin-arm64 ./cmd/tdd

.PHONY: vuln
vuln:
	@echo "==> govulncheck"
	@$(GOBIN)/govulncheck ./...

.PHONY: boundary
## internal/store must never appear in the import graph of cmd/td.
boundary:
	@echo "==> import boundary"
	@go test ./internal/boundary/

.PHONY: schema
## openapi.yaml has to keep validating and keep matching the mux.
schema:
	@echo "==> openapi schema lint"
	@go test ./internal/server/ -run TestOpenAPI

.PHONY: tools
## Install the pinned tools check needs.
tools:
	go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	@command -v golangci-lint >/dev/null || \
		go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: seed
## Load testdata/seed.json into a scratch database, including its fixed clock,
## so every fixture evaluates the way the case files say it does.
seed:
	@mkdir -p $(BUILD_DIR)
	@rm -f $(DEV_DB) $(DEV_DB)-wal $(DEV_DB)-shm
	@go run ./cmd/tdd -db $(DEV_DB) -seed $(SEED)

.PHONY: run
## Serve the seeded database on the fixture's own clock, so a filter typed at
## a running server returns what the case files say it should.
run:
	@mkdir -p $(BUILD_DIR)
	@go run ./cmd/tdd -db $(DEV_DB) -now @$(SEED) -addr 127.0.0.1:8080 -base-url http://127.0.0.1:8080

.PHONY: run-live
## The same server on the real clock, for anything that is not fixture work.
run-live:
	@mkdir -p $(BUILD_DIR)
	@go run ./cmd/tdd -db $(DEV_DB) -addr 127.0.0.1:8080 -base-url http://127.0.0.1:8080

.PHONY: account
## Create the one account. There is no signup page and no route that makes one.
account:
	@mkdir -p $(BUILD_DIR)
	@go run ./cmd/tdd -db $(DEV_DB) account create

.PHONY: token
## Mint a token for the CLI: make token NAME=tui SCOPES=read,write
token:
	@go run ./cmd/tdd -db $(DEV_DB) token create -name "$(or $(NAME),tui)" -scopes "$(or $(SCOPES),read,write,capture)"

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: clean
clean:
	rm -rf $(BUILD_DIR)
