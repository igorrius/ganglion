# Backups of a node: what there is to back up, and where it can go

- **Date:** 2026-11-17
- **Task:** P1 · M7, daily backups with a tested restore
  ([#244](https://github.com/igorrius/ganglion/issues/244),
  [ADR-0095](../adr/0095-daily-encrypted-backups-to-an-s3-compatible-bucket.md))
- **Question:** how large is a node's data, which of it needs a backup,
  and which destinations fit a one-person node that may run on Railway or
  on any Linux machine?
- **Status:** read and measured; sources were read on the date above.
  Items marked *(from memory)* were not checked against a source that day.

## What a node keeps

Measured on the owner's node on Railway, 2026-11-17, a week after the
first start:

| Path in the data directory | Size | Backed up? |
|---|---|---|
| `ganglion.db` with `-wal` and `-shm` | 3.3 MB + 4.0 MB WAL | Yes: everything the node remembers (history, cells, modules' records, the journal, encrypted secrets) |
| `workspace/` (a git repository) | 1.2 MB | Yes: persona, memory topics, module sources, the history of `ganglion.yaml` |
| `modules/` | 1.0 MB | Yes: installed `.wasm` files the database describes; `modules/templates` is rewritten at each start |
| `ganglion.yaml` | 4 KB | Yes |
| `master.key` | 32 bytes | **No**: kept apart, off the node (ADR-0077) |
| `releases/` | 107 MB | No: signed releases are downloaded again |
| `logs/` | 0.4 MB | No |

Everything that needs a backup is under 10 MB. The image has no `sqlite3`
binary; the node's own driver (`modernc.org/sqlite`) can run `VACUUM
INTO`, which writes a consistent copy while the node runs.

## Destinations

**Railway volume backups** ([backups](https://docs.railway.com/volumes/backups),
[volume API](https://docs.railway.com/integrations/api/manage-volumes)):
manual and scheduled (daily, weekly, monthly) copies of a volume, labelled
"under development". Retention is fixed: a daily copy is kept 6 days, a
weekly one 27, a monthly one 89. Restore is of the whole volume only, into
the same project and environment, as a new volume the service is
redeployed onto. A copy cannot be downloaded, and wiping the volume
deletes its copies. The docs say nothing about the consistency of a live
SQLite file. A forum answer says the Hobby plan has no backups; not
confirmed by the docs.

**S3-compatible object storage:**

- *Backblaze B2* ([pricing](https://www.backblaze.com/cloud-storage),
  [application keys](https://www.backblaze.com/docs/cloud-storage-application-keys)):
  the first 10 GB free. An application key can be limited to one bucket
  and given `writeFiles` without `deleteFiles`, so the node can write a
  copy and cannot remove one. Object Lock is free.
- *Cloudflare R2* ([pricing](https://developers.cloudflare.com/r2/pricing/),
  [bucket locks](https://developers.cloudflare.com/r2/buckets/bucket-locks/),
  [tokens](https://developers.cloudflare.com/r2/api/tokens/)): 10 GB-month
  free, no egress fees. Tokens are read-only or read and write, scoped to
  a bucket; there is no write-only token. A bucket lock rule blocks
  deleting and overwriting under a prefix for a retention period.
- *AWS S3* *(from memory)*: an IAM policy with `s3:PutObject` alone is
  write-only; versioning and Object Lock keep old copies.

**A private git repository** ([deploy keys](https://docs.github.com/en/authentication/connecting-to-github-with-ssh/managing-deploy-keys)):
a deploy key with write access can rewrite history unless a ruleset
blocks force-pushes and deletion. A database in git grows the history
with every copy.

## Tools

- **Litestream** ([repository](https://github.com/benbjohnson/litestream),
  [how it works](https://litestream.io/how-it-works/)): continuous
  replication of a SQLite database to object storage, Apache-2.0, marked
  beta; v0.5 changed the format and cannot restore v0.3 copies. It is
  documented as a separate process. It covers the database only.
- **restic, Kopia, rclone** *(from memory)*: separate binaries, with no
  stable Go library.
- An S3 `PutObject` is one signed HTTP request (AWS Signature Version 4).
  A bucket with Object Lock wants a checksum of the body on the request
  (`Content-MD5` or `x-amz-checksum-*`) *(from memory)*.

## What this means for the decision

The data is small enough to copy whole every day. A copy made by the node
works on any host. A destination is safe from a compromised node only
when the node's credential cannot delete or overwrite: a B2 key without
`deleteFiles`, an S3 policy with `PutObject` alone, or an R2 bucket lock.
