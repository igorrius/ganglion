# Install

## From a container image

Prebuilt images are not published yet. They are tracked in [issue 179](https://github.com/igorrius/ganglion/issues/179). Until then, build the image yourself from a clone of the repository:

```sh
git clone https://github.com/igorrius/ganglion.git
cd ganglion
docker build -t ganglion .
```

The image is about 1.2 GB. Most of it is the Rust toolchain that module builds need. To leave the `claude` CLI out, add `--build-arg CLAUDE_CLI=off`.

### Run it

The node keeps all its data in `/data`. Mount a volume there and pass your settings as environment variables:

```sh
docker run -d --name ganglion --restart on-failure \
  -v ganglion-data:/data \
  -e TELEGRAM_BOT_TOKEN=... \
  -e CLAUDE_CODE_OAUTH_TOKEN=... \
  ganglion
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
