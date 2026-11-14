# Install

## From a container image

Each release is published as an image for `linux/amd64` and `linux/arm64` (a Raspberry Pi 4 or 5 with a 64-bit system):

```sh
docker pull ghcr.io/igorrius/ganglion:latest
```

The tags are:

| Tag | Points at |
|---|---|
| `latest` | The newest release |
| `<major>.<minor>`, for example `0.1` | The newest release of that line |
| `<version>`, for example `0.1.0` | Exactly that release; it never moves |

In 0.x a new minor version can change the configuration or stored formats, so pin a line or a version once you rely on the node. The [package page](https://github.com/igorrius/ganglion/pkgs/container/ganglion) lists every published version. `ganglion version` inside the container prints the release tag.

The image is about 1.2 GB. Most of it is the Rust toolchain that module builds need. It also holds the `claude` CLI.

To check that an image was built by the project's release workflow from the repository, use the GitHub CLI:

```sh
gh attestation verify oci://ghcr.io/igorrius/ganglion:latest -R igorrius/ganglion
```

### Build the image yourself

Build it from a clone when you want unreleased code or an image without the `claude` CLI (`--build-arg CLAUDE_CLI=off`):

```sh
git clone https://github.com/igorrius/ganglion.git
cd ganglion
docker build -t ganglion .
```

Then use `ganglion` instead of `ghcr.io/igorrius/ganglion:latest` in the commands below.

### Run it

The node keeps all its data in `/data`. Mount a volume there and pass your settings as environment variables:

```sh
docker run -d --name ganglion --restart on-failure \
  -v ganglion-data:/data \
  -e TELEGRAM_BOT_TOKEN=... \
  -e CLAUDE_CODE_OAUTH_TOKEN=... \
  ghcr.io/igorrius/ganglion:latest
```

Things to know:

- The node runs as the user `ganglion`, uid 10001. If you start the container as root, it hands `/data` over to that user and drops privileges. If you start it with `--user 10001`, the volume must already belong to uid 10001.
- Do not mount a single file (such as `ganglion.yaml`) into the container. The node rewrites its configuration file, which needs a directory. Put the file inside `/data`.
- `TELEGRAM_BOT_TOKEN` is the bot token. `CLAUDE_CODE_OAUTH_TOKEN` is the login for the `claude` CLI (see [Models](../running/models.md)). For an OpenAI-compatible service, pass the key variable your configuration names, for example `OPENROUTER_API_KEY`.
- Set `telegram.admin` in `/data/ganglion.yaml` before the first conversation. See [Configure](configure.md).
- `--restart on-failure` matters for `/restart`: the node then exits with code 75, and nothing in the image starts it again by itself.
- Run any command that touches `/data` as uid 10001, never as root. A file created by root there can lock the node out.

```sh
docker exec -u 10001 ganglion ganglion secret list --db /data/ganglion.db
```

To set a secret this way, add `-i` to `docker exec` so the value reaches standard input.

## From source

```sh
git clone https://github.com/igorrius/ganglion.git
cd ganglion
make build
./bin/ganglion version
```

`make build` writes the program to `bin/ganglion`. `ganglion version --json` prints the version in a form you can paste into a bug report.

Next: [Configure](configure.md).
