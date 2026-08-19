MODULE := github.com/k8s-ai/k8s-ai
BINARY := k8s-ai
BUILD_DIR := bin
IMAGE ?= k8s-ai

VERSION ?= v1.0.2
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.Date=$(DATE)

.PHONY: build build-linux test test-race vet lint docker clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/k8s-ai

# 交叉编译 Linux amd64（Windows 上可直接使用，纯 Go 无需 gcc）
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-amd64 ./cmd/k8s-ai

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

lint: vet
	@files=$$(gofmt -l $$(find cmd internal prompts tests -name '*.go')); \
	if [ -n "$$files" ]; then echo "gofmt needed:"; echo "$$files"; exit 1; fi

docker:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		-t $(IMAGE):$(VERSION) .

clean:
	rm -rf $(BUILD_DIR)