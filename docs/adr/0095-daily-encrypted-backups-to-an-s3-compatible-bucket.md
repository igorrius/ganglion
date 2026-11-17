# ADR-0095: Back up the node daily, encrypted with a key from its master key, to an S3-compatible bucket

- **Status:** Accepted
- **Date:** 2026-11-17
- **Deciders:** Igor Artasevych
- **Spec:** [7.8 Security and operations](../spec.md#78-security-and-operations-mvp)
  (R-BAK-1 – R-BAK-6), [10.2](../spec.md#102-p1--all-in-one--scope-and-acceptance)

## Context

A node keeps everything it remembers on one data directory: the SQLite
database, the workspace git repository, the installed modules and its
settings file. The spec's risk table has asked for a daily backup since
P0 ("Lost volume or corrupted DB"), and the phase plan never cut it. No
backup exists: the guide tells the operator to copy files by hand.

Since the cutover of 2026-11-13 the owner's family bot runs on one node,
and the old bot is gone, so a lost volume loses the family's history,
memory and modules with nothing to fall back to
([#244](https://github.com/igorrius/ganglion/issues/244)).

Facts ([research note](../research/2026-11-17-backup-destinations.md)):

- What needs a backup is under 10 MB on the owner's node: the database
  (3.3 MB plus its WAL), the workspace (1.2 MB), the modules (1 MB) and
  `ganglion.yaml`. Releases (107 MB) and logs do not.
- The image has no `sqlite3` binary; the node's driver can run `VACUUM
  INTO`, a consistent copy of a live database.
- The secret store's rows are encrypted with `master.key` (ADR-0077),
  which must be kept apart from the database's backup.
- The node runs on Railway today, and on any Linux machine by design
  (ADR-0087). Railway's own volume backups keep a daily copy 6 days,
  cannot be downloaded, restore only into the same project, and are lost
  with the volume.
- The archive holds a family's conversations and memory: it leaves the
  node, so it is encrypted before it does.

Owner's choices (2026-11-17): an S3-compatible bucket; the archive
encrypted with a key derived from `master.key`; no Railway volume backups
as a second layer.

## Options

### A. The platform's volume backups

Turn on Railway's daily schedule. No code; Railway only; short fixed
retention; no copy outside the platform.

### B. The node uploads an encrypted archive to an S3-compatible bucket

Once a day the node makes a consistent copy of its data, packs it,
encrypts it, and puts it in a bucket of the operator's provider (Backblaze
B2, Cloudflare R2, AWS S3 or any S3-compatible store) with a credential
that can write and, where the provider allows it, cannot delete.

### C. The node pushes to a private git repository

The workspace is already git; the database and modules are added as
files. Each copy of the database grows the history, and a write key can
rewrite it unless the repository's rules forbid it.

### D. Continuous replication with Litestream

Replicates the database to object storage within seconds. A separate,
beta process that covers the database only; the workspace and modules
still need B or C.

## Comparison

| Criterion | A | B | C | D |
|---|---|---|---|---|
| Works on any host | No | Yes | Yes | Yes |
| Survives the loss of the volume and the platform account | No | Yes | Yes | Yes |
| A compromised node cannot destroy older copies | Yes | With a write-only key or a bucket lock | Only with a ruleset against force-push | As B |
| Encrypted before it leaves the node | No | Yes | Only if built | No |
| Data lost in the worst case | A day | A day | A day | Seconds (database only) |
| Built here | Nothing | Archive, encryption, one signed PUT, restore | Archive, encryption, git push | Nothing for the database; B for the rest |
| New dependency | None | None | None | Litestream |

## Decision

**B.** The node backs itself up once a day to an S3-compatible bucket,
with no new dependency.

- **What:** a consistent copy of the database made with `VACUUM INTO`,
  the whole `workspace/` with its `.git`, `modules/` without
  `modules/templates`, and `ganglion.yaml`, in one `tar` archive with a
  manifest (the node's version, the time, the database's schema version,
  the SHA-256 of every file), compressed with gzip. Never `master.key`,
  `releases/`, `logs/` or the claude work folder.
- **Checked before it leaves:** the copy passes `PRAGMA integrity_check`;
  a failed check is a failed backup, not an upload.
- **Encryption:** the key is HKDF-SHA256 of the master key with the info
  `ganglion-backup-v1` and a random salt per archive, kept in the
  archive's header. The archive is sealed with AES-256-GCM in 64 KiB
  chunks; a chunk's nonce is a random prefix, the chunk's counter and a
  last-chunk flag, so a reordered, cut or extended archive does not open.
  This is the primitive the secret store already uses (`secrets/vault`),
  with the chunking of the STREAM construction; `age` was considered and
  not taken, because it would add a dependency for a format the node
  writes and reads alone.
- **Upload:** one `PutObject` over HTTPS, signed with AWS Signature
  Version 4 by a small client of our own, with `Content-MD5` so a bucket
  with Object Lock accepts it. The object is named
  `<prefix>/ganglion-<UTC time>.tar.gz.enc`. The node never lists, reads or
  deletes objects: retention is the bucket's lifecycle rule and lock,
  which the operator sets at the provider. The guide recommends a key
  that cannot delete (B2 without `deleteFiles`, S3 with `PutObject`
  alone) or a bucket lock (R2).
- **When:** daily at `backup.at` (node time, default `03:00`), and at
  once with `ganglion backup now` on the host. A failure alerts the admins
  once, and its recovery once; `/status` shows the last backup and warns
  when the last success is older than 36 hours; a metric records it.
- **Restore:** `ganglion backup restore <archive>` on the host, with the
  master key given the usual way (R-CFG-10), into an empty data
  directory. It opens every chunk before it writes a file, checks the
  manifest's hashes and the database's integrity, and writes nothing on a
  failure. The operator downloads the archive with the provider's own
  tool: the node's credential cannot read.
- **Configuration:** a `backup:` section in `ganglion.yaml` (endpoint,
  region, bucket, prefix, the access key's id, the secret key as
  `env:NAME` or `store:NAME`, the time). Without the section backups are
  off and `/status` says so. The section is not on `config.propose`'s
  allowlist (R-CFG-14) and the agent has no tool that changes it or
  starts a backup; `config.history` may show its fields, which hold no
  secret value.
- **The master key:** a backup is worth nothing without the key. The
  guide and the Railway runbook tell the operator to keep `master.key`
  (or `GANGLION_MASTER_KEY`) off the node, in a password manager, before
  turning backups on.
- **Proof:** a lab scenario backs up a node to a local S3-compatible
  server, restores the archive into a new data directory, starts a node
  on it and finds its memory, modules and cells. Once, the owner restores
  a backup of the live node on a fresh node by the runbook.

## Security

- **Trust boundaries:** the node and the storage provider (the provider
  holds ciphertext only); the bucket credential on the node; an archive
  coming back to `ganglion backup restore`.
- **What an attacker controls:** the storage provider or anyone who reads
  the bucket; the network; the agent, if a model is talked into it; a node
  someone has broken into; an archive file handed to restore.
- **Abuse cases and what stops them:**
  - *The bucket is read.* It holds ciphertext under a key derived from
    the master key, which never leaves the node and is not in the archive.
  - *An archive is changed, cut, reordered or swapped for another node's.*
    Every chunk is authenticated, the last one is marked, and the key is
    the node's own: restore refuses it before writing anything.
  - *The agent redirects backups to its own bucket, exfiltrating the
    history.* The `backup:` section is outside `config.propose`'s
    allowlist, no tool touches it, and host commands cannot write the
    settings file (R-CMD-3).
  - *A broken-into node deletes or overwrites the older copies.* The node
    never deletes; with a write-only key or a bucket lock it cannot.
    Copies made before the break-in survive; it can stop making new ones,
    and the 36-hour warning shows that.
  - *A crafted archive escapes the data directory on restore.* An archive
    opens only under the node's key; restore still refuses absolute paths,
    `..`, links and devices, and writes only into an empty directory.
  - *The credential leaks from the node.* It can only write into one
    bucket; the secret key is a `env:` or `store:` secret, redacted like
    every other.

## Consequences

- A lost volume costs at most a day: the operator restores the last
  archive with the master key they kept.
- The operator must keep the master key off the node; a node whose key
  is only on the lost volume cannot be restored. The guide says so where
  backups are turned on.
- The node gains a small S3 client, an archive format with a version, and
  two host commands; the guide's manual backup section becomes the
  fallback for a node without a bucket.
- A change of the archive's format takes a new version in its header;
  restore reads every version it has ever written.
- Up to a day of data can be lost: a smaller loss is a reason to look at
  Litestream again.

## Revisit when

A day of lost data is too much, the data outgrows a daily full copy, or a
second node needs the same archive (P2 replication).
