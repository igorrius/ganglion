# The ganglion node with a Rust toolchain for module builds (R-RUN-9, R-LS-4).
# Build:  docker build -t ganglion:m4 .
# Run:    docker run -v ganglion-data:/data ganglion:m4
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

# The node's user has a real home: the builder keeps its builds under the
# user's cache folder (/home/ganglion/.cache/ganglion/builds). It must have
# no ~/.cargo/config.toml, which would refuse every build.
RUN useradd --uid 10001 --create-home --home-dir /home/ganglion --shell /usr/sbin/nologin ganglion \
 && chmod 0755 /home/ganglion \
 && mkdir /data && chown ganglion:ganglion /data

COPY --from=build /out/ganglion /usr/local/bin/ganglion
COPY examples/ /app/examples/

# Rustup must never try to install or update at run time: its home is read-only.
ENV RUSTUP_AUTO_INSTALL=0 \
    HOME=/home/ganglion
USER ganglion
WORKDIR /data
VOLUME /data
ENTRYPOINT ["ganglion", "run", "--db", "/data/ganglion.db"]
