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
3. Tag and push:

   ```sh
   git tag -a vX.Y.Z -m vX.Y.Z origin/master
   git push origin vX.Y.Z
   ```

4. Both workflows end green; the release page lists five files.

A tag that is not on `master`, or not of the form above, fails the
workflows and publishes nothing.

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
