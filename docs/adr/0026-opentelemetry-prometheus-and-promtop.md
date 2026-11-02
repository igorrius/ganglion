# ADR-0026: Instrument with OpenTelemetry; serve metrics to Prometheus with the promtop dashboard, export traces over OTLP

- **Status:** Accepted
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** [7.9](../spec.md#79-observability) (R-OBS-1–4), principle 8

## Context

The first live run showed that a running node said nothing after start:
per-turn logs were at debug level. That is fixed with one info line per
turn. The owner wants more: logs, traces and metrics, and a local
dashboard. The owner's own [promtop](https://github.com/igorrius/promtop)
reads a Prometheus `/metrics` endpoint and has a web dashboard that a
service can embed (package `dash`). Tracing in promtop may come later.

The spec had "OpenTelemetry instead of slog + `turns`" on its "Not now"
list.

Measured on 2026-11-02 with three minimal HTTP servers, Go 1.26.3,
`CGO_ENABLED=0`:

| Build | Binary | RSS idle | Packages |
|---|---|---|---|
| `net/http` only | 7.9 MB | 4.8 MB | 17 |
| + `prometheus/client_golang` | 12.1 MB | 8.1 MB | 63 |
| + OpenTelemetry SDK: metrics through its Prometheus exporter, traces over OTLP/HTTP | 22.4 MB | 13.0 MB | 222 |

The node itself is 18.3 MB on disk and 25 MB RSS after 1,000 turns.

## Options

### A. OpenTelemetry for metrics and traces

- **Instrumentation:** the code uses the OpenTelemetry API.
- **Metrics:** the SDK's Prometheus exporter serves `/metrics`.
- **Traces:** go out over OTLP/HTTP when the standard environment
  variables point at a collector.
- **Logs:** slog lines carry trace and span ids.
- **Dashboard:** the promtop web dashboard is mounted beside `/metrics`.

### B. Prometheus metrics now with `client_golang`, OpenTelemetry later

This costs about half as much. Adding traces later means redoing the
instrumentation.

### C. Code against the OpenTelemetry API; SDK and exporters behind a build tag

The default binary barely grows, but there are two builds, and the
default one has no metrics.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Metrics for promtop | Yes | Yes | Only in the tagged build |
| Traces | Yes, OTLP to any backend | Later, with rework | Only in the tagged build |
| One instrumentation model | Yes | No | Yes |
| Cost on the node | ~+15 MB binary, ~+8 MB RSS | ~+4 MB, ~+3 MB | ~0 by default |
| Dependencies | ~100 modules | ~20 | ~100 in the tagged build |

## Decision

**A.** The owner chose it.
- **Exposition.** `/metrics` (Prometheus text) and `/webmetrics/` (the
  promtop dashboard, `dash.Localhost`) are served on
  `GANGLION_METRICS_ADDR`, default `127.0.0.1:9464`; `off` disables them.
- **Traces.** They are exported only when `OTEL_EXPORTER_OTLP_ENDPOINT` or
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` is set; otherwise the tracer is a
  no-op.
- **What stays out of telemetry.** No message text or model output, in
  spans, logs or metric labels.
- **Labels.** Metric labels stay low-cardinality: provider, model,
  outcome, kind, transport, method. Conversations and principals never
  become labels.
- **Go version.** `promtop/dash` declares `go 1.27.3`, so ganglion moved
  from Go 1.26.3 to 1.27.3 on the same day.

## Consequences

- The node grows by roughly 15 MB on disk and 8 MB in RSS, which is still
  far inside 128 MB (principle 8). `make memory` re-measures it.
- The dependency count jumps from four direct modules to about a hundred.
  Versions are pinned to releases at least two weeks old where possible.
- **No trace across stages.** Spans of one event across the inbox, the
  turn and the outbox are not one trace yet. They share the `event.id`
  attribute, and carrying trace context through the queue is a later
  step.
- The `turns` table stays: it is the durable per-turn record that limits
  and memory read. Telemetry is for watching the node.

## Revisit when

- The node must fit a small edge device (P2), where OpenTelemetry may move
  behind a build tag there (option C).
- promtop gains tracing.
