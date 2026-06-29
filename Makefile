.PHONY: help build install test test-race test-pkg vet fmt clean

help:
	@echo "Targets:"
	@grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN {FS=":.*?##"}; {printf "  %-12s %s\n", $$1, $$2}'
	@echo "Run \`make -n <target>\` to see what a target does."

BINARY   := bin/session-indexer
TEST_PKG ?= ./...
VERSION  := $(shell git describe --tags --exact-match 2>/dev/null || echo "0.1.0")
LDFLAGS  := -ldflags "-X main.version=$(VERSION)"

build: ## build to bin/session-indexer
	go build $(LDFLAGS) -o $(BINARY) ./cmd/session-indexer

install: ## go install (puts binary on PATH)
	go install $(LDFLAGS) ./cmd/session-indexer

test: ## go test ./...
	go test $(TEST_PKG)

test-race: ## go test -race ./...
	go test -race $(TEST_PKG)

test-pkg: ## go test one package — requires PKG=, e.g. make test-pkg PKG=./internal/mine
	@test -n "$(PKG)" || (echo "PKG is required, e.g. make test-pkg PKG=./internal/mine" && exit 1)
	go test $(PKG)

vet: ## go vet ./...
	go vet ./...

fmt: ## gofmt -w . (mutating)
	gofmt -w .

clean: ## remove bin/session-indexer
	rm -f $(BINARY)
