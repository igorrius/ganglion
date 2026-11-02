VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint check live-echo live-agent memory modules modules-tinygo modules-rust modrun modbench bench

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/ganglion ./cmd/ganglion

test:
	go test -race ./...

lint:
	golangci-lint run

check: lint test build

# The M2 echo bot on the owner's test bot (docs/README.md, owner setup).
# .env holds TELEGRAM_TEST_BOT_TOKEN and GANGLION_ADMIN; stop any other
# process polling that bot first, or this one gets 409 and stops polling.
# A second instance needs its own database: make live-echo LIVE_DB=bin/live2.db
LIVE_DB ?= bin/live.db

live-echo: build
	@test -f .env || { echo "live-echo: no .env (see docs/README.md, owner setup)"; exit 1; }
	@set -a; . ./.env; set +a; \
	test -n "$$TELEGRAM_TEST_BOT_TOKEN" || { echo "live-echo: TELEGRAM_TEST_BOT_TOKEN is not set in .env"; exit 1; }; \
	TELEGRAM_BOT_TOKEN=$$TELEGRAM_TEST_BOT_TOKEN TELEGRAM_BOT_TOKEN_FILE= GANGLION_DB=$(LIVE_DB) ./bin/ganglion --roles core

# The M3a agent on the test bot: live-echo plus a model catalogue
# (models.local.yaml, git-ignored; see models.example.yaml). Provider keys
# named by key_env come from .env.
LIVE_MODELS ?= models.local.yaml

live-agent: build
	@test -f .env || { echo "live-agent: no .env (see docs/README.md, owner setup)"; exit 1; }
	@test -f $(LIVE_MODELS) || { echo "live-agent: no $(LIVE_MODELS); copy models.example.yaml"; exit 1; }
	@set -a; . ./.env; set +a; \
	test -n "$$TELEGRAM_TEST_BOT_TOKEN" || { echo "live-agent: TELEGRAM_TEST_BOT_TOKEN is not set in .env"; exit 1; }; \
	TELEGRAM_BOT_TOKEN=$$TELEGRAM_TEST_BOT_TOKEN TELEGRAM_BOT_TOKEN_FILE= GANGLION_DB=$(LIVE_DB) \
	GANGLION_MODELS_FILE=$(LIVE_MODELS) ./bin/ganglion --roles core

# RSS of the echo bot and of the agent on a fake model API, idle and after
# 1,000 messages through the fake Bot API.
memory:
	GANGLION_MEASURE=1 go test -count=1 -run 'TestMemory(Echo|Agent)' -v ./cmd/ganglion/

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
