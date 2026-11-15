# The ganglion node with a Rust toolchain for module builds (R-RUN-9, R-LS-4).
# Releases publish it as ghcr.io/igorrius/ganglion for amd64 and arm64
# (ADR-0086, .github/workflows/image.yml, checked by scripts/check-image.sh).
# Build:  docker build -t ganglion .
# Run:    docker run -v ganglion-data:/data ganglion
# The runbook is docs/deploy-railway.md.

FROM golang:1.27 AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-X github.com/igorrius/ganglion/internal/buildinfo.version=${VERSION}" \
    -o /out/ganglion .

FROM debian:stable-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl build-essential \
 && rm -rf /var/lib/apt/lists/*

# Rust lives in /opt/rust: readable by all, writable by root only, so a build
# the node runs cannot change the toolchain (ADR-0064). Builds link host
# programs (build.rs), hence a C linker (build-essential).
ENV RUSTUP_HOME=/opt/rust/rustup \
    CARGO_HOME=/opt/rust/cargo \
    PATH=/opt/rust/cargo/bin:$PATH
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \
    | sh -s -- -y --no-modify-path --profile minimal --default-toolchain stable --target wasm32-wasip1 \
 && chmod -R a+rX /opt/rust

# The claude CLI for the claude-cli provider (ADR-0021), as one root-owned
# binary the node cannot change. It logs in with CLAUDE_CODE_OAUTH_TOKEN from
# the environment and keeps its own files under the user's home. Skip it with
# --build-arg CLAUDE_CLI=off when the node uses another provider.
ARG CLAUDE_CLI=stable
RUN if [ "$CLAUDE_CLI" != off ]; then \
      export HOME=/tmp/claude-install \
   && curl -fsSL https://claude.ai/install.sh | bash -s "$CLAUDE_CLI" \
   && install -m 0755 "$(readlink -f "$HOME/.local/bin/claude")" /usr/local/bin/claude \
   && rm -rf "$HOME" \
   && claude --version; \
    fi
ENV DISABLE_AUTOUPDATER=1

# The node's user has a real home: the builder keeps its builds under the
# user's cache folder (/home/ganglion/.cache/ganglion/builds). It must have
# no ~/.cargo/config.toml, which would refuse every build.
RUN useradd --uid 10001 --create-home --home-dir /home/ganglion --shell /usr/sbin/nologin ganglion \
 && chmod 0755 /home/ganglion \
 && mkdir /data && chown ganglion:ganglion /data

COPY --from=build /out/ganglion /usr/local/bin/ganglion
# The level of this image's environment (R-UPD-3, ADR-0081): a release whose
# release.json asks for a higher env is refused until the image is rebuilt.
COPY update/image-env /usr/local/share/ganglion/image-env
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

# Rustup must never try to install or update at run time: its home is read-only.
ENV RUSTUP_AUTO_INSTALL=0 \
    HOME=/home/ganglion
# The image starts as root only for docker-entrypoint.sh, which hands /data to
# ganglion and drops to uid 10001 before the node starts. Running the image
# with --user 10001 skips the hand-over and needs a volume that user owns.
# The node runs in /data so the workspace (agent.workspace, "workspace" by
# default, relative to this folder) is on the volume. The sandbox then denies
# the database, its journal files and lock, and ganglion.yaml one by one
# rather than the whole folder (sandbox.DenyList).
# No VOLUME line: Railway refuses a Dockerfile that has one. Mount a volume
# at /data yourself (docker run -v, or a Railway volume).
WORKDIR /data
ENTRYPOINT ["docker-entrypoint.sh", "ganglion", "run", "--db", "/data/ganglion.db"]
