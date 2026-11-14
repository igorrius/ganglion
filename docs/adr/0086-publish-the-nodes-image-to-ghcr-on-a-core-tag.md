# ADR-0086: Publish the node's image to GHCR on a core tag

- **Status:** Accepted
- **Date:** 2026-11-14
- **Deciders:** Igor Artasevych
- **Spec:** [7.15](../spec.md#715-updating-the-node) (R-UPD-8),
  [14](../spec.md#14-not-now); [ADR-0081](0081-update-the-node-from-signed-releases-at-the-owners-command.md),
  [ADR-0082](0082-open-the-repository-now-and-keep-the-announced-release-in-p3.md),
  [ADR-0064](0064-builds-write-only-their-temp-folder.md)

## Context

Since the repository opened (ADR-0082), colleagues install nodes on their
own machines. The only install paths are a Go build from source and
`docker build` of the [`Dockerfile`](../../Dockerfile), which takes several
minutes and pulls the Rust toolchain and the `claude` CLI on the user's
machine. ADR-0082 named "an image published by tag as the install path" as
the next step for outside readers
([#179](https://github.com/igorrius/ganglion/issues/179)).

What bears on the decision:

- **The core release is a tag `vX.Y.Z` on a commit of `master`**
  (ADR-0081, R-UPD-1). Its signed binaries and `/update` (task 6.7) are not
  built yet; nothing else runs on a core tag today. Module tags
  (`html2md/v…`, ADR-0041) are separate and must not trigger this.
- **The image** is `golang:1.27` for the build and `debian:stable-slim`
  with Rust (`wasm32-wasip1`) under `/opt/rust`, the `claude` CLI, an
  unprivileged user (uid 10001) and an entrypoint that hands `/data` to it.
  About 1.2 GB. Both base images are published for `linux/amd64` and
  `linux/arm64`; rustup and `claude.ai/install.sh` detect the architecture.
- **The version** comes from `-X …/internal/buildinfo.version=$VERSION`
  (`ARG VERSION`, default `dev`). `.dockerignore` keeps `.git` out of the
  build context, so Go's VCS stamps (commit, time) are not in the image's
  binary; the version string is all `ganglion version` reports.
- **Targets** are a server or a VPS (amd64) and a Raspberry Pi (arm64,
  spec principle 8).
- **The repository is public.** GitHub Actions gives public repositories
  hosted arm64 runners (`ubuntu-24.04-arm`) and artifact attestations at no
  cost.
- **Supply chain:** CI already pins third-party actions by commit SHA
  (`dtolnay/rust-toolchain` in `ci.yml`) and gives each workflow
  `contents: read` by default.
- **Scope:** the spec's "Not now" list held "packaging and install paths
  for other users: a Docker image, …" until P3. The owner moved the image
  forward with ADR-0082; the rest of that item (a Railway template, a Helm
  chart, a systemd unit, `ganglion init`) stays.

## Options

Three questions, each with its options; the comparison is per question.

**Registry.**

- **A1. GitHub Container Registry** (`ghcr.io/igorrius/ganglion`).
  The workflow logs in with its own `GITHUB_TOKEN` and `packages: write`.
- **A2. Docker Hub** (`docker.io/<account>/ganglion`). An account and an
  access token kept as a repository secret.
- **A3. Both**, the same digests pushed twice.

**Architectures.**

- **B1. amd64 only.** A Pi builds the image itself.
- **B2. amd64 and arm64 under QEMU** on one amd64 runner, one
  `buildx --platform linux/amd64,linux/arm64` call.
- **B3. amd64 and arm64 on native runners**, one job per platform
  pushed by digest, a last job that joins them into one multi-platform
  index.

**Tags.**

- **C1. Exact only:** `X.Y.Z`.
- **C2. `X.Y.Z`, `X.Y` and `latest`**, where `X.Y` moves only to the
  newest release of its line and `latest` only to the newest release of
  all; a pre-release (`vX.Y.Z-rc.1`) gets its exact tag only.
- **C3. C2 plus `X`.**

## Comparison

| Registry | A1. GHCR | A2. Docker Hub | A3. Both |
|---|---|---|---|
| Secrets to keep | None: `GITHUB_TOKEN` | An account token in the repository | A token |
| Anonymous pulls | Public package, no pull limit that a node meets | Rate-limited per IP for anonymous pulls | Either |
| Link to the source | Automatic through `org.opencontainers.image.source` | A description kept by hand | Both |
| Provenance next to the image | Artifact attestations on GitHub | Not with GitHub's attestations | Split |
| What the owner does once | Make the package public after the first push | Create an account and a repository | Both |

| Architectures | B1. amd64 | B2. QEMU | B3. Native runners |
|---|---|---|---|
| A Pi installs without building | No | Yes | Yes |
| Build time of arm64 | — | Long: Go, rustup and the `claude` installer emulated | As amd64 |
| The smoke test runs on the real CPU | amd64 only | Emulated | Both |
| Workflow size | One job | One job | A matrix and a join job, digests passed between them |

| Tags | C1. Exact | C2. Exact, minor, latest | C3. + major |
|---|---|---|---|
| A guide line that keeps working | Must name a version | `latest` works; pinning is one edit | Same as C2 |
| A patch on an old line moves `latest` back | — | No: only the newest release moves it | No |
| Meaning in 0.x | Clear | `0.Y` follows a minor line, which may break in 0.x but not within it | `0` is every release: no meaning |

## Decision

**A1, B3, C2.** The image goes to GHCR because it needs no secret and
keeps the image, its source and its attestation in one place; arm64 is
built on GitHub's arm64 runners because they are free for this public
repository and they run the smoke test on the CPU a Pi has.

**The workflow** ([`.github/workflows/image.yml`](../../.github/workflows/image.yml)):

- Runs on a tag `v*.*.*` (publishes), on a pull request that changes the
  `Dockerfile`, `docker-entrypoint.sh`, `.dockerignore` or the workflow,
  and by hand (both build and check, push nothing). A tag that is not
  `vX.Y.Z[-pre]` or not on `master` fails before anything is built.
- One job per platform (`ubuntu-24.04`, `ubuntu-24.04-arm`) builds the
  image from the existing `Dockerfile` with `VERSION` set to the tag
  (`git describe` otherwise), checks it, and on a tag pushes it by digest.
- **The check** before a push: `ganglion version --json` names the tag
  and the platform's architecture; `cargo`, the `wasm32-wasip1` target and
  `claude` run as uid 10001; and the node starts with the guide's minimum
  configuration (a placeholder token and admin, `models` through the
  `claude` CLI) and reaches the point where Telegram refuses the token,
  which shows the image reads the guide's file and starts its components.
- A last job joins the two digests into one index with the tags of C2,
  computed from the repository's release tags; it then attests the index's provenance on GitHub and pushes
  the attestation next to the image.
- **Labels and annotations:** the OCI keys `source`, `version`,
  `revision`, `created`, `licenses`, `title`, `description` and `url`, as
  image labels on each platform and as annotations on the index (GHCR
  reads the index's).

**Not part of this:** the signed binaries, `release.json` and `/update` of
ADR-0081. When task 6.7 builds them, the release workflow for binaries
runs on the same tag beside this one, and the image gains the `env` number
ADR-0081 describes as a label. Updating a node that runs from the image
is, until then, a pull of a newer tag and a restart.

## Security

- **Trust boundary:** the image is what a user runs as their node, so the
  workflow that pushes it is inside the trust root. What can write to
  `ghcr.io/igorrius/ganglion`: the owner's account and this workflow's
  token, with `packages: write` only in the jobs that push and only on a
  tag event.
- **A compromised action** in a job with a write token could push a
  different image. Every action in the jobs that push is pinned by commit
  SHA. The image is built and pushed with the `docker` and `buildx` that
  the runner ships, not with a third-party action.
- **A tag pushed from a branch** (a commit not reviewed on `master`) is
  refused by the tag check, as for modules (ADR-0041).
- **A pull request from a fork** runs the build with a read-only token
  and never pushes; the push step is limited to tag events.
- **A tampered image in the registry** is detectable: the index carries a
  GitHub artifact attestation signed by the workflow's identity, checked
  with `gh attestation verify oci://ghcr.io/igorrius/ganglion:<version> -R igorrius/ganglion`.
  BuildKit's own provenance is attached to each platform image as well.
- **What the image installs at build time** (rustup, the `claude`
  installer, Debian packages) is fetched over TLS at each release, as it is
  for a local build today. A release therefore carries whatever those
  sources served at that moment; the attestation records when and from
  which commit.

## Consequences

- Installing a node is `docker run ghcr.io/igorrius/ganglion:<version>`
  on amd64 or arm64, without Go, Rust or a build.
- `ganglion version` in the image reports the release tag; the commit is
  in the image's `org.opencontainers.image.revision` label.
- A change to the `Dockerfile` is built and checked on both architectures
  in its pull request.
- The first push creates the package as private. The owner makes it
  public once in the package's settings; until then the guide's command
  fails for everyone else.
- A release costs two image builds of a few minutes each and one more
  workflow to keep. The base images, the Rust toolchain and the `claude`
  CLI in an image are as old as its tag: a security fix in them reaches
  users only with a new release.

## Revisit when

GHCR limits public pulls or the hosted arm64 runners stop being free for
public repositories; a user asks for a Docker Hub image or another
architecture (RISC-V, armv7); or task 6.7 lands and the image's
environment number needs to be part of the tag scheme.
