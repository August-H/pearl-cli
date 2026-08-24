MODULE := github.com/August-H/pearl-cli
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE := $(shell date -u +%Y-%m-%d)
LDFLAGS := -s -w \
	-X $(MODULE)/cli.version=$(VERSION) \
	-X $(MODULE)/cli.commit=$(COMMIT) \
	-X $(MODULE)/cli.date=$(DATE)

.PHONY: build install test

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o pearl ./cmd/pearl

install: build
	install -m 755 pearl "$$(go env GOPATH)/bin/pearl"

test:
	go vet ./... && go test ./...
