BIN := bin/ipmi-connect

# Prefer the copy in $GOPATH/bin: golangci-lint must be built with a Go at least as new as go.mod.
GOLANGCI_LINT ?= $(firstword $(wildcard $(shell go env GOPATH)/bin/golangci-lint) golangci-lint)

.PHONY: all build test test-go test-js lint fmt clean

all: lint test build

build:
	go build -o $(BIN) ./cmd/ipmi-connect

test: test-go test-js

test-go:
	go test -race ./...

test-js:
	node --test 'internal/web/jstest/*.test.mjs'

lint:
	go vet ./...
	$(GOLANGCI_LINT) run ./...

fmt:
	$(GOLANGCI_LINT) fmt ./...

clean:
	rm -rf bin
