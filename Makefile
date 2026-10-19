APP_NAME := a2matex-runtime
BUILD_DIR := bin
VERSION ?= dev
COMMIT ?= unknown
BUILD_TIME ?= unknown
LDFLAGS := -X a2matex-runtime/internal/buildinfo.Version=$(VERSION) \
	-X a2matex-runtime/internal/buildinfo.Commit=$(COMMIT) \
	-X a2matex-runtime/internal/buildinfo.BuildTime=$(BUILD_TIME)

.PHONY: build fmt lint run wire clean

build:
	go build -buildvcs=false -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) ./cmd/agent

fmt:
	gofmt -s -w cmd internal

lint:
	golangci-lint run ./...

run:
	go run -buildvcs=false ./cmd/agent

wire:
	wire ./cmd/agent

clean:
	go clean
