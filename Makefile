VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint check modules modules-tinygo modrun

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/ganglion ./cmd/ganglion

test:
	go test -race ./...

lint:
	golangci-lint run

check: lint test build

# WASM modules. TinyGo is optional: make modules-tinygo needs it on PATH.
modules:
	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o bin/deye-reader.go.wasm ./modules/deye/wasm

modules-tinygo:
	tinygo build -target=wasip1 -buildmode=c-shared -opt=2 -no-debug -o bin/deye-reader.tinygo.wasm ./modules/deye/wasm

modrun:
	go build -o bin/modrun ./cmd/modrun
