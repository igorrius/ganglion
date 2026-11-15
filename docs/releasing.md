# Releasing the core

How a core release is made and checked, and how the key that signs it is
kept (R-UPD-1, R-UPD-8,
[ADR-0081](adr/0081-update-the-node-from-signed-releases-at-the-owners-command.md),
[ADR-0086](adr/0086-publish-the-nodes-image-to-ghcr-on-a-core-tag.md)).

## What a core tag makes

A tag `vX.Y.Z` (or `vX.Y.Z-<pre>`) on a commit of `master` runs two
workflows:

- **`release.yml`** builds `ganglion-linux-amd64` and
  `ganglion-linux-arm64` (no cgo), writes `release.json` and
  `SHA256SUMS`, signs `SHA256SUMS` and publishes the files as the tag's
  GitHub release:

  | File | What |
  |---|---|
  | `ganglion-linux-amd64`, `ganglion-linux-arm64` | The node's binary |
  | `release.json` | `version` (the tag), `commit`, `env` (below) |
  | `SHA256SUMS` | The sums of the three files above |
  | `SHA256SUMS.sig` | A raw Ed25519 signature over `SHA256SUMS` |

  Before it publishes, the workflow checks the signature with `openssl`
  against the keys in [`update/keys`](../update/keys) and the whole
  release with `tools/releasecheck`, the node's own check.
- **`image.yml`** publishes the image `ghcr.io/igorrius/ganglion:X.Y.Z`.

## Make a release

1. `master` is green.
2. When the code needs a change of the image's environment (a new Rust
   target, a new entrypoint), raise the number in `update/image-env` in
   the same pull request. A node refuses a release whose `env` is above
   its image's until the image is rebuilt.
3. Fetch first, check the commit carries the release workflow and a
   key, then tag and push:

   ```sh
   git fetch origin
   git ls-tree --name-only origin/master .github/workflows/release.yml update/keys/
   git tag -a vX.Y.Z -m vX.Y.Z origin/master
   git push origin vX.Y.Z
   ```

   A tag cannot be deleted or moved (a repository rule): a tag on the
   wrong commit is left as it is, and the next version is released.
   `v0.2.0` is such a tag, made before the release workflow was merged;
   it has no release and no image.

4. Both workflows end green; the release page lists five files. Check
   them as below.

A tag that is not on `master`, or not of the form above, fails the
workflows and publishes nothing.

## How a node takes a release

A release changes no running node. The owner sends `/update` in the admin's
private chat, then `/update install vX.Y.Z` (or runs `ganglion update install
vX.Y.Z` on the host), and the node:

1. asks GitHub, on its own hosts only, for that tag's release and downloads
   the files above (the binary at most 256 MiB, a whole install at most five
   minutes);
2. checks `SHA256SUMS` against the embedded keys, the binary and
   `release.json` against it, that `release.json` names the tag, that the
   version is newer than the running one, that the platform has a binary and
   that `env` is not above the image's (`update/image-env`, copied into the
   image; a node run outside an image skips this check);
3. copies the database to `<data>/releases/<version>/before.db`, moves the
   release into place and records it as pending in `state.json`;
4. restarts (exit 75). The launcher at the start of `run` verifies the active
   release once more and runs it. The release has two starts and 2 minutes of
   running to confirm itself; otherwise the node goes back to the previous
   version, restores `before.db` when the failed version had migrated the
   database, and alerts the admins.

Only the version, the date and a link to the release page are ever shown;
what a release's page says is not read. `/update` lists the newest stable
release, and a pre-release is installed only by naming its tag. A node built
without a release version (`dev`, a `git describe` string) is not updated: it
moves once to the image of a release.

## Check a release by hand

```sh
openssl pkeyutl -verify -pubin -inkey update/keys/<key>.pem -rawin \
  -in SHA256SUMS -sigfile SHA256SUMS.sig
sha256sum -c SHA256SUMS
go run ./tools/releasecheck -dir <folder> -tag vX.Y.Z
```

## The signing key

The private key is the secret `RELEASE_SIGNING_KEY` of the GitHub
environment `release`, which only tags `v*.*.*` may deploy to. It is not
in the repository and not on a developer's machine, unless the owner kept
an offline backup. The public keys are `update/keys/*.pem`; the node
embeds all of them and accepts a release any of them signed.

**The first key.** As a maintainer, with `gh` logged in:

```sh
scripts/release-key.sh release-<yyyymmdd> [<offline backup file>]
```

It writes `update/keys/release-<yyyymmdd>.pem`, sets up the environment
and its tag rule, and sets the secret. Commit the public key; the next
tag is signed with it.

**Replacing a key.** A node accepts only keys that the release it runs
already embeds, so a new key arrives in two releases:

1. `scripts/release-key.sh --key-only <new name> <file>` makes the new
   key: its public key in `update/keys` beside the old one, the private
   one in the file, offline, and the secret left as it is. Commit the
   public key and release. This release is signed with the old key and
   carries both public keys.
2. Set the new key as the secret and remove the file:

   ```sh
   gh secret set RELEASE_SIGNING_KEY --repo igorrius/ganglion --env release < <file>
   ```

   Remove the old public key from `update/keys` and release again, now
   signed with the new key.

A node that skipped step 1's release cannot verify step 2's; it takes
step 1's release first. **A stolen or lost key** has no such path: remove
its public key, make a new one, release, and move every node to that
release with a rebuilt image (`railway up` or a new image), since the
binary in the image is the floor that a node falls back to.
