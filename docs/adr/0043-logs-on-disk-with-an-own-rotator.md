# ADR-0043: Write logs to disk through a small own rotator, synchronously

- **Status:** Accepted
- **Date:** 2026-11-03
- **Deciders:** Igor Artasevych
- **Spec:** [7.9](../spec.md#79-observability) (R-OBS-6, R-OBS-7); design section 6.4 ([M3b and M3c design](../plans/p1-m3b-m3c-design.md)); [ADR-0026](0026-opentelemetry-prometheus-and-promtop.md), [ADR-0027](0027-written-go-conventions-and-lint-guardrails.md); plan task 5 ([M3c plan](../plans/p1-m3c-modules-skills-diagnostics.md))

## Context

Logs go to stdout today. That is right for Railway, journald and Docker,
but `ganglion mcp` (R-OBS-7) must read them with no running node, and a
Pi with no log collector loses them.

R-OBS-6 asks for logs on disk with these properties:
- JSON lines in `<data>/logs/`;
- synchronous writes, so the last lines before a `kill -9` survive;
- rotation by size, with limits on the file count and age, so the disk
  never fills;
- secrets redacted.

What the rotator must do:
- write each record with one `write(2)`;
- rotate before a write that would pass the size limit;
- prune old files by count and age.

The conventions prefer no dependency when the code is small, and no
goroutine whose lifetime nobody owns.

The owner's `go-app-tui-template` logging was reviewed (design 6.4). It
rotates with `lestrrat-go/file-rotatelogs`, archived since 2021 and
time-only, and it writes asynchronously through a bus, which loses the
queue on `kill -9`. Its configuration shape (sink, format, rotation) is
reused.

## Options

### A. `gopkg.in/natefinch/lumberjack.v2` (v2.2.1)

The common Go choice: an `io.WriteCloser` that rotates by size, with
`MaxBackups`, `MaxAge` and optional compression.

### B. `lestrrat-go/file-rotatelogs`

Rotation by time pattern.

### C. An own rotator (package `logfile`)

About 150 lines with the following shape:
- `Write` under a mutex, straight to the file;
- rotation by rename before a write that would pass the limit;
- the prune of `ganglion-*.log` by count and age in the calling
  goroutine;
- an injected clock.

## Comparison

| Criterion | A. lumberjack | B. file-rotatelogs | C. Own |
|---|---|---|---|
| Synchronous write | Yes | Yes | Yes |
| Size limit | Yes | No (time only) | Yes |
| Count and age limits | Yes | Age | Yes |
| Background goroutines | The mill goroutine that prunes and compresses starts on first use and does not end with `Close` | One per rotation check | None |
| Testable time | `currentTime` package variable | Clock option | Injected clock (conventions) |
| Maintained | Last release v2.2.1, 2023 | Archived | Ours |
| Dependency | One module | One module | None |

## Decision

**C** (owner, 2026-11-03).
- The behaviour is small and fully specified.
- It has no goroutine nobody owns.
- It follows the conventions (an injected clock, no package state).

The defaults:
- 10 MiB a file, 5 rotated files, 14 days;
- rotated files named `ganglion-<UTC yyyymmddThhmmss.mmmZ>.log`;
- directories `0700`, files `0600`;
- the prune touches only files with the rotated name.

The logger fans out with `slog.NewMultiHandler`: text to stdout, JSON to
the rotator. Both sides pass through the redact handler, which removes:
- the exact values of the secrets the node read;
- the journal's patterns (Bot API tokens, bearer tokens, passwords in
  URLs, key-value pairs).

A failed disk write is counted (`ganglion_log_write_errors_total`) and
never fails the stdout side. `--log-dir off` keeps stdout only.

## Security

- **Trust boundaries.** None new: the files are the node's own, in its
  data directory.
- **What an attacker controls.** The content of log lines that quote
  outside text (a page title, an error from a remote API).
- **Abuse cases:**
  - *A secret reaches a log line.* The redact handler covers the
    message and every string attribute, groups included.
  - *Outside text in a log misleads a model that reads it.*
    `logs.search` results are wrapped as data and taint the session (plan
    task 6).
  - *A flood of log lines fills the disk.* The count and size limits cap
    the total.
  - *Another process reads the logs.* The files are `0600` in a `0700`
    directory.

## Consequences

- `ganglion mcp` and the agent can search logs after a crash.
- `kill -9` loses nothing the kernel has received. A power loss can
  lose the page cache: no `fsync` per line, by choice. The cost would
  be every log call.
- No compression of rotated files; with the defaults the total stays
  under 60 MiB.

## Revisit when

- Logs need shipping off the node (P2's error journal over JetStream,
  R-OBS-9).
- The volume makes compression worth a goroutine.
