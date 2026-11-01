VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint check modules modules-tinygo modules-rust modrun modbench bench

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/ganglion ./cmd/ganglion

test:
	go test -race ./...

lint:
	golangci-lint run

check: lint test build

# WASM modules: deye-reader for both ABIs (own, extism) and three
# toolchains (Go, TinyGo, Rust). TinyGo and Rust are optional and must be on
# PATH; they are compared in ADR-0011 but not built in CI.
ABIS := own extism

modules:
	for abi in $(ABIS); do GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o bin/deye-reader.$$abi-go.wasm ./modules/deye/$$abi || exit 1; done

modules-tinygo:
	for abi in $(ABIS); do tinygo build -target=wasip1 -buildmode=c-shared -opt=2 -no-debug -o bin/deye-reader.$$abi-tinygo.wasm ./modules/deye/$$abi || exit 1; done

modules-rust:
	for abi in $(ABIS); do (cd modules/deye-rs && cargo build --quiet --release --target wasm32-wasip1 --features $$abi) && cp modules/deye-rs/target/wasm32-wasip1/release/deye_reader.wasm bin/deye-reader.$$abi-rust.wasm || exit 1; done

modrun:
	go build -o bin/modrun ./cmd/modrun

modbench:
	go build -o bin/modbench ./cmd/modbench

# Measure every built deye-reader variant, one process each.
bench: modbench
	for f in bin/deye-reader.*.wasm; do abi=$$(basename $$f .wasm | cut -d. -f2 | cut -d- -f1); \
	  ./bin/modbench -wasm $$f -abi $$abi -manifest modules/deye/deye-reader.yaml \
	    -replay cmd/modbench/testdata/deye-replay.json -config inverter_sn=2400000001 -n 300 || exit 1; done
