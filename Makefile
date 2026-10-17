BIN := bin/ipmi-connect

# Prefer the copy in $GOPATH/bin: golangci-lint must be built with a Go at least as new as go.mod.
GOLANGCI_LINT ?= $(firstword $(wildcard $(shell go env GOPATH)/bin/golangci-lint) golangci-lint)

.PHONY: all build test lint fmt clean

all: lint test build

build:
	go build -o $(BIN) ./cmd/ipmi-connect

test:
	go test -race ./...

lint:
	go vet ./...
	$(GOLANGCI_LINT) run ./...

fmt:
	$(GOLANGCI_LINT) fmt ./...

clean:
	rm -rf bin
