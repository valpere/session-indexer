.PHONY: build install test test-race test-pkg vet fmt clean

BINARY := bin/session-indexer
TEST_PKG ?= ./...

build:
	go build -o $(BINARY) ./cmd/session-indexer

install:
	go install ./cmd/session-indexer

test:
	go test $(TEST_PKG)

test-race:
	go test -race $(TEST_PKG)

test-pkg:
	@test -n "$(PKG)" || (echo "PKG is required, e.g. make test-pkg PKG=./internal/mine" && exit 1)
	go test $(PKG)

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -f $(BINARY)
