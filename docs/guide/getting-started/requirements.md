# Requirements

## The machine

- **Linux.** ganglion does not run on other systems.
- **Kernel 6.2 or later** if you want host commands and module builds. They use Landlock, a Linux sandbox feature. Check with `uname -r`. On an older kernel the node still starts and answers, but it refuses module builds; without Landlock at all it runs no host commands either.
- **Not as root.** The sandbox and module builds are off for the root user. Run the node as an ordinary user. The container image already does this.
- **Memory.** The node itself needs about 50 MB when idle. Building a module needs about 1 GB.

## Accounts and keys

- **A Telegram bot token.** Talk to [@BotFather](https://t.me/BotFather) and create a bot.
- **Your Telegram user id**, a number. If you do not know it, write to your bot once before you set `telegram.admin`. The node does not answer a stranger, but it logs a warning, `access: event dropped`, with `principal=telegram:user:` and your id. Any Telegram bot made to show your user id works too.
- **Access to a model.** Either:
  - a `claude` CLI installed and logged in on the machine (a Claude subscription), or
  - an API key for any OpenAI-compatible service (OpenAI, OpenRouter, a local Ollama, and so on).

## For a source build

- Go 1.27.
- Rust (stable) with the `wasm32-wasip1` target, and a C linker (`build-essential` on Debian). The node needs Rust at run time to build modules. Without it the node works but offers no module builds.
- Make sure no `~/.cargo/config.toml` exists for the node's user. The module builder refuses to build when it finds one.

```sh
rustup target add wasm32-wasip1
```

## Container option

Docker (or a compatible runtime) is enough. The image carries the node, Rust and the `claude` CLI, so you need none of the source-build tools. See [Install](install.md).
