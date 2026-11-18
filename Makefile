APP_NAME := a2matex-runtime
BUILD_DIR := bin
A2MATEX_RUNTIME_ADB_EXECUTABLE_PATH ?= $(HOME)/Documents/platform-tools/adb
VERSION ?= 0.1.0
COMMIT ?= unknown
BUILD_TIME ?= unknown
LDFLAGS := -X github.com/A2MaTex/a2matex-runtime/internal/buildinfo.Version=$(VERSION) \
	-X github.com/A2MaTex/a2matex-runtime/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/A2MaTex/a2matex-runtime/internal/buildinfo.BuildTime=$(BUILD_TIME)

.PHONY: build test fmt lint run migrate-up wire clean

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
	A2MATEX_RUNTIME_ADB_EXECUTABLE_PATH="$(A2MATEX_RUNTIME_ADB_EXECUTABLE_PATH)" go run -buildvcs=false ./cmd/agent

migrate-up:
	go run -buildvcs=false ./cmd/agent --migrate-up

wire:
	wire ./cmd/agent

clean:
	go clean
