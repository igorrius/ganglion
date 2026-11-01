VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint check modules modules-tinygo modules-rust modrun modbench bench

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/ganglion ./cmd/ganglion

test:
	go test -race ./...

lint:
	golangci-lint run

check: lint test build

# The deye-reader module, built with three toolchains (Go, TinyGo, Rust) to
# compare them (ADR-0011). TinyGo and Rust are optional, must be on PATH,
# and are not built in CI.
modules:
	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o bin/deye-reader.go.wasm ./modules/deye/wasm

modules-tinygo:
	tinygo build -target=wasip1 -buildmode=c-shared -opt=2 -no-debug -o bin/deye-reader.tinygo.wasm ./modules/deye/wasm

modules-rust:
	cd modules/deye-rs && cargo build --quiet --release --target wasm32-wasip1
	cp modules/deye-rs/target/wasm32-wasip1/release/deye_reader.wasm bin/deye-reader.rust.wasm

modrun:
	go build -o bin/modrun ./cmd/modrun

modbench:
	go build -o bin/modbench ./cmd/modbench

# Measure every built deye-reader variant, one process each.
bench: modbench
	for f in bin/deye-reader.*.wasm; do \
	  ./bin/modbench -wasm $$f -manifest modules/deye/deye-reader.yaml \
	    -replay cmd/modbench/testdata/deye-replay.json -config inverter_sn=2400000001 -n 300 || exit 1; done
