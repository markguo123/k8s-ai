MODULE := github.com/k8s-ai/k8s-ai
BINARY := k8s-ai
BUILD_DIR := bin

VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.Date=$(DATE)

.PHONY: build test test-race vet lint clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/k8s-ai

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

lint: vet
	gofmt -l . | (! grep .)

clean:
	rm -rf $(BUILD_DIR)