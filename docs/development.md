# Development notes

Setup that only someone working on ganglion needs. A user running a node
reads [the guide](https://igorrius.github.io/ganglion/) instead.

## Owner setup for live checks

What the owner sets up by hand before a live run on the test bot. Nothing
here is needed for tests: they run against a fake Bot API.

- **Test bot:** a bot of its own, used only by ganglion (`@test_bot`
  below). Its
  token is `TELEGRAM_TEST_BOT_TOKEN`. Two runs at once get `409 Conflict`,
  and the older one stops polling (R-DLV-8).
- **Your Telegram user id:** needed for the admin entry in the access list
  (`telegram.admin: telegram:user:<id>` in the settings file); strangers get no reply. Any
  "user info" bot shows it, or the `from.id` of your message in
  `getUpdates`.
- **Running locally:** a `.env` file in the repository root (git-ignored); copy
  [`.env.example`](https://github.com/igorrius/ganglion/blob/master/.env.example), which also lists the optional variables:

  ```sh
  TELEGRAM_TEST_BOT_TOKEN=<token from @BotFather>
  ```

  Settings go in `ganglion.local.yaml` (git-ignored): copy
  [`ganglion.example.yaml`](https://github.com/igorrius/ganglion/blob/master/ganglion.example.yaml) and set `telegram.admin` to
  `telegram:user:<your id>`. For `make live-echo`, set it in
  [`examples/ganglion.echo.yaml`](https://github.com/igorrius/ganglion/blob/master/examples/ganglion.echo.yaml) instead.

- **Running in a Claude Code cloud session:** a local `.env` does not reach
  the container. In the cloud environment's settings (the environment menu
  in the session title bar → Edit):
  1. Network access: add `api.telegram.org` to Allowed domains, keeping
     "Allow package managers" ticked
     ([docs](https://code.claude.com/docs/en/cloud-environments#network-access)).
  2. Add `TELEGRAM_TEST_BOT_TOKEN` as a secret or environment variable. A new session picks it up.
     Never paste the token into the chat. Settings go in a `ganglion.yaml` the session can read.

## Releasing a module

A built-in module ships in the binary and is updated on a running node
without a new core ([ADR-0041](adr/0041-module-install-update-and-trust-boundary.md)):

1. Change `modules/html2md`, run `make modules-html2md`, commit the
   rebuilt `html2md.wasm` with its sources.
2. Push a tag `html2md/vX.Y.Z`. The `Module release` workflow rebuilds
   it, checks it matches the commit, runs the fixtures and the self-test,
   and publishes the `.wasm`, the manifest and `SHA256SUMS`.
3. On the node: `/modules update html2md`; `/modules rollback html2md`
   goes back; `/modules` lists the versions and why one was refused.

A release that asks for a capability, more memory or time than the
core's ceiling, or another ABI, is refused until the core is upgraded.

## Reading in Obsidian

The `docs/` folder works as an Obsidian vault: graph view, backlinks and
quick switching come for free, and Mermaid diagrams render natively.

1. Open `docs/` as a vault ("Open folder as vault").
2. Settings → Files and links: turn off **Use [[Wikilinks]]** and set
   **New link format** to **Relative path to file**, so new links keep
   working on GitHub ([ADR-0002](adr/0002-docs-as-markdown-and-mermaid.md)).

`docs/.obsidian/` is git-ignored, so vault settings stay local.
