.PHONY: help build install test test-race test-pkg vet fmt clean eval

help:
	@echo "Targets:"
	@grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN {FS=":.*?##"}; {printf "  %-12s %s\n", $$1, $$2}'
	@echo "Run \`make -n <target>\` to see what a target does."

BINARY   := bin/session-indexer
TEST_PKG ?= ./...
VERSION  := $(shell git describe --tags --exact-match 2>/dev/null || echo "0.2.0")
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

eval: ## retrieval-quality eval on LoCoMo (needs Ollama + bge-m3 and tmp/eval/data/locomo10.json; not part of `make test`)
	go run ./cmd/session-indexer-eval $(EVAL_ARGS)

fmt: ## gofmt -w . (mutating)
	gofmt -w .

clean: ## remove bin/session-indexer
	rm -f $(BINARY)
