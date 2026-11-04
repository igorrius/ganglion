VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint check live-echo live-agent accept compare crash-agent memory modules modules-html2md modules-tinygo modules-rust clean-modules modrun modbench bench bench-html2md

build:
	go build -ldflags "-X github.com/igorrius/ganglion/internal/buildinfo.version=$(VERSION)" -o bin/ganglion .

test:
	go test -race ./...
	go test -race -tags lab ./internal/chatlab/... ./tools/chatlab/...

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
	TELEGRAM_BOT_TOKEN=$$TELEGRAM_TEST_BOT_TOKEN TELEGRAM_BOT_TOKEN_FILE= GANGLION_DB=$(LIVE_DB) ./bin/ganglion run --roles core

# The M3a agent on the test bot: live-echo plus a model catalogue
# (models.local.yaml, git-ignored; see models.example.yaml) and the agent's
# workspace (./workspace, git-ignored; copy examples/workspace for a
# persona). Provider keys named by key_env come from .env.
LIVE_MODELS ?= models.local.yaml
LIVE_WORKSPACE ?= workspace

live-agent: build
	@test -f .env || { echo "live-agent: no .env (see docs/README.md, owner setup)"; exit 1; }
	@test -f $(LIVE_MODELS) || { echo "live-agent: no $(LIVE_MODELS); copy models.example.yaml"; exit 1; }
	@set -a; . ./.env; set +a; \
	test -n "$$TELEGRAM_TEST_BOT_TOKEN" || { echo "live-agent: TELEGRAM_TEST_BOT_TOKEN is not set in .env"; exit 1; }; \
	TELEGRAM_BOT_TOKEN=$$TELEGRAM_TEST_BOT_TOKEN TELEGRAM_BOT_TOKEN_FILE= GANGLION_DB=$(LIVE_DB) \
	GANGLION_MODELS_FILE=$(LIVE_MODELS) GANGLION_WORKSPACE=$(LIVE_WORKSPACE) ./bin/ganglion run --roles core

# The scripted acceptance of M3b (config/accept_test.go): the real core
# with real models and the real web, the fake Bot API playing the owner.
# ACCEPT_ROUTE picks a route: cli, the main model through the claude CLI,
# logged in (the default, ADR-0047); api (OPENROUTER_API_KEY from .env) or
# both (ACCEPT_ROUTE=). Transcripts and node logs go to bin/accept/. It takes
# a few minutes and spends subscription limits.
ACCEPT_ROUTE ?= cli
accept: build
	@mkdir -p bin/accept
	@set -a; [ -f .env ] && . ./.env; set +a; \
	GANGLION_LIVE_ACCEPT=1 GANGLION_RELAY=$(CURDIR)/bin/ganglion GANGLION_ACCEPT_OUT=$(CURDIR)/bin/accept \
	go test -count=1 -timeout 60m -run 'TestAcceptance/$(ACCEPT_ROUTE)' -v ./config/

# Run the same conversations through several models of the catalogue and
# write a report (D-6). COMPARE picks provider:model pairs; empty = all.
COMPARE ?=
compare:
	@set -a; [ -f .env ] && . ./.env; set +a; \
	go run ./tools/modelcompare -models $(LIVE_MODELS) -only "$(COMPARE)" -out bin/compare.md && echo "report: bin/compare.md"

# Test the agent by talking to it (ADR-0048): real models over the fake Bot API,
# a table of what passed. LAB picks scenarios by name; empty = all. It spends
# subscription limits, and does not read .env: that file holds TELEGRAM_*.
LAB ?=
lab: build
	go build -tags lab -o bin/chatlab ./tools/chatlab
	bin/chatlab run $(LAB)

# The agent under kill -9: 200 kills by default, GANGLION_CRASH_ITERS sets
# more. A fake Bot API and a fake model; it spends a few minutes (3.8).
crash-agent:
	GANGLION_CRASH_AGENT=1 go test -count=1 -timeout 30m -run 'TestCrashHarnessAgent$$' -v ./cmd/

# RSS of the echo bot and of the agent on a fake model API, idle and after
# 1,000 messages through the fake Bot API; the agent also with a tool call
# in every turn; and the node's peak after one hostile 1 MiB page through
# web.fetch and html2md.
memory:
	GANGLION_MEASURE=1 go test -count=1 -run 'TestMemory(Echo|Agent|AgentTools|Metrics)$$' -v ./cmd/
	GANGLION_MEASURE=1 go test -count=1 -run 'TestMemoryHostilePage' -v ./config/

# The modules. html2md (ADR-0035) is a Rust crate whose build,
# modules/html2md/html2md.wasm, is committed and embedded in the binary, so
# a Go build needs no Rust; rebuilding it needs rustup, which installs the
# toolchain pinned in modules/html2md/rust-toolchain.toml. Paths are
# remapped so the module does not carry the builder's directories.
modules: modules-html2md
	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o bin/deye-reader.go.wasm ./modules/deye/wasm

modules-html2md:
	cd modules/html2md && \
	RUSTFLAGS="-C target-feature=+simd128 --remap-path-prefix=$${CARGO_HOME:-$$HOME/.cargo}=/cargo --remap-path-prefix=$(CURDIR)=/src" \
	cargo build --quiet --release --locked --target wasm32-wasip1
	cp modules/html2md/target/wasm32-wasip1/release/html2md.wasm modules/html2md/html2md.wasm

# The deye-reader module is also built with TinyGo and Rust to compare
# toolchains (ADR-0011). Both are optional, must be on PATH, and are not
# built in CI.

modules-tinygo:
	tinygo build -target=wasip1 -buildmode=c-shared -opt=2 -no-debug -o bin/deye-reader.tinygo.wasm ./modules/deye/wasm

modules-rust:
	cd modules/deye-rs && cargo build --quiet --release --target wasm32-wasip1
	cp modules/deye-rs/target/wasm32-wasip1/release/deye_reader.wasm bin/deye-reader.rust.wasm

# Cargo keeps every build of the Rust modules in their target/ folders:
# host builds from `cargo test`, clippy and examples grow to gigabytes,
# while `make modules` needs only target/wasm32-wasip1. This drops them all;
# the next build fills them again. bin/ (the live database) is not touched.
clean-modules:
	cd modules/html2md && cargo clean --quiet
	cd modules/deye-rs && cargo clean --quiet

modrun:
	go build -o bin/modrun ./tools/modrun

modbench:
	go build -o bin/modbench ./tools/modbench

# Measure html2md on each saved page, one process each (ADR-0035).
bench-html2md: modbench
	for f in modules/html2md/testdata/*.html; do \
	  ./bin/modbench -wasm modules/html2md/html2md.wasm -manifest modules/html2md/html2md.yaml -html $$f -n 100 || exit 1; done

# Measure every built deye-reader variant, one process each.
bench: modbench
	for f in bin/deye-reader.*.wasm; do \
	  ./bin/modbench -wasm $$f -manifest modules/deye/deye-reader.yaml \
	    -replay tools/modbench/testdata/deye-replay.json -config inverter_sn=2400000001 -n 300 || exit 1; done
