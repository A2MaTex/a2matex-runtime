APP_NAME := a2matex-runtime
BUILD_DIR := bin
VERSION ?= 0.0.0-dev
COMMIT ?= unknown
BUILD_TIME ?= unknown
LDFLAGS := -X github.com/A2MaTex/a2matex-runtime/internal/buildinfo.Version=$(VERSION) \
	-X github.com/A2MaTex/a2matex-runtime/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/A2MaTex/a2matex-runtime/internal/buildinfo.BuildTime=$(BUILD_TIME)

.PHONY: build test fmt lint run wire clean

build:
	mkdir -p $(BUILD_DIR)
	go build -buildvcs=false -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) ./cmd/agent

test:
	go test ./...

fmt:
	gofmt -s -w cmd internal pkg

lint:
	golangci-lint run ./...

run:
	go run -buildvcs=false ./cmd/agent

wire:
	wire ./cmd/agent

clean:
	go clean
