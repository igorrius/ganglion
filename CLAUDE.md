# ganglion

Distributed agent network in Go. Start from [docs/spec.md](docs/spec.md):
it defines what we build, the requirements (`R-<area>-<n>`), and the phases
with their gates. When a decision changes, update the spec first, then the
code.

## Rules

- **All documentation is written in English**: README, everything under
  `docs/`, code comments, doc comments, and Markdown files in the repository.
  This holds even when the conversation is in another language.
- Stay inside the current phase of the spec. An idea outside it goes to the
  spec's "Not now" list instead of into the code.
