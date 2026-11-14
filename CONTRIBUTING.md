# Contributing

ganglion is 0.x and built by one person on weekends
([status](README.md#status)). The most useful contribution today is a bug
report from someone who installed a node and tried it.

## Reporting a bug

Open a [GitHub issue](https://github.com/igorrius/ganglion/issues/new/choose)
with the output of `ganglion version`, what you did, what you expected and
what happened. Remove tokens, Telegram ids and the address of your node
from anything you paste. A vulnerability is not a bug report: see
[SECURITY.md](SECURITY.md).

Ids of the form `G-<n>` in the documents are cards of the maintainer's own
backlog, which is not public. They are labels, not links.

## Pull requests

Ask in an issue before writing anything larger than a fix: the project
follows a phase plan, and an idea outside the current phase goes to the
spec's "Not now" list instead of into the code. A pull request may wait, or
be declined because of the phase alone.

Only the maintainer merges, and every pull request needs the checks to
pass.

The rules, in brief; the full text is in [CLAUDE.md](CLAUDE.md) (written
for the models that work here, and holding for people too) and
[docs/conventions.md](docs/conventions.md):

- **Run `make check`** (lint, tests, build) before you push. It needs Go
  1.27 and golangci-lint v2; module tests need Rust with the
  `wasm32-wasip1` target and are skipped without it.
- **Everything is written in English:** code, comments, documents, commit
  messages.
- **The spec comes first.** A change in behaviour updates
  [docs/spec.md](docs/spec.md) before the code.
- **A significant decision needs an ADR** that compares at least two
  options: a new dependency, a protocol or storage format, a security
  boundary. Copy [docs/adr/template.md](docs/adr/template.md). Accepted
  ADRs are not rewritten.
- **The architecture documents change in the same commit as the code:** a
  new or moved package updates
  [docs/architecture/05-code-map.md](docs/architecture/05-code-map.md).
- **Security is part of the change.** Everything from outside the process
  is untrusted; a change that crosses a trust boundary comes with a test
  that feeds the hostile input
  ([conventions, section 11](docs/conventions.md#11-security)).
- **Nothing real about a running node goes into the repository:** no
  address, bot name, Telegram id, device serial or secret, in code, tests,
  documents or commit messages. Use placeholders: `example.com`,
  `telegram:user:123456`, `@test_bot`.

By contributing you agree that your work is licensed under the
[Apache License 2.0](LICENSE).

<!-- protections test, not to be merged -->
