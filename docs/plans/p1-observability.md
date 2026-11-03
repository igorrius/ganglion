# P1 · Observability — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans
> to implement this plan task by task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** see what a node does: one info log line per turn (done), metrics
in Prometheus format with the promtop dashboard, and OTLP traces when a
collector is configured.

**Spec:** [7.9](../spec.md#79-observability) (R-OBS-1–4);
[ADR-0026](../adr/0026-opentelemetry-prometheus-and-promtop.md).

## Global constraints

- No message text or model output in spans, logs or metric labels.
- Metric labels are low-cardinality: provider, model, outcome, kind,
  transport, method. Never conversations or principals.
- Pin new modules to releases at least two weeks old where one exists.
- Tests read metrics through the exporter's Prometheus text, the way
  promtop does.

## Metrics

Prometheus names after the exporter's conversion:

| Name | Type | Labels |
|---|---|---|
| `ganglion_turns_total` | counter | `outcome` |
| `ganglion_turn_duration_seconds` | histogram | `outcome` |
| `ganglion_model_calls_total` | counter | `provider`, `model`, `result` (ok or error kind) |
| `ganglion_model_call_duration_seconds` | histogram | `provider`, `model` |
| `ganglion_model_tokens_total` | counter | `provider`, `model`, `kind` (input, cached, output) |
| `ganglion_model_cost_usd_total` | counter | `provider`, `model` |
| `ganglion_outbox_sent_total` | counter | `transport` |
| `ganglion_outbox_retries_total` | counter | `transport`, `kind` (retry_after, error) |
| `ganglion_outbox_failed_total` | counter | `transport` |
| `ganglion_telegram_api_duration_seconds` | histogram | `method`, `code` |
| `ganglion_access_dropped_total` | counter | `reason` (stranger, malformed) |

## Spans

| Span | Attributes |
|---|---|
| `turn` | `event.id`, `event.kind`, `outcome`, `steps` |
| `model.call` | `provider`, `model`, `result`, token counts |
| `tool.call` | `tool`, `ok` |
| `outbox.send` | `message.id`, `transport`, `parts` |
| `telegram <method>` | `http.status_code` |

### Task 1: the `telemetry` package — done

**Files:** `telemetry/telemetry.go`, `telemetry/log.go`, `telemetry/telemetry_test.go`

- `Setup(ctx, Config{Addr, Service, Version}) (*Telemetry, error)`:
  - a MeterProvider with the OpenTelemetry Prometheus exporter on its own
    registry;
  - a TracerProvider with an OTLP/HTTP exporter when the OTLP environment
    variables are set, otherwise no-op;
  - the HTTP server on `Addr` serving `/metrics` and `/webmetrics/`
    (promtop `dash`, `dash.Localhost`).

  `Shutdown(ctx)` flushes and stops.
- `LogHandler(h slog.Handler) slog.Handler` adds `trace_id` and `span_id`
  when the record's context holds a valid span.
- Tests:
  - `TestMetricsEndpoint` (a counter appears in `/metrics`);
  - `TestDashboardLocalOnly` (`/webmetrics/` answers on loopback);
  - `TestNoOTLPNoTracer` (spans are not recording);
  - `TestLogHandlerAddsIDs`;
  - `TestMetricsOff` (`Addr` "off": no listener).

### Task 2: instrument core, agent, outbox, Telegram, access — done

- Each package takes meters and tracers from the global providers
  (`otel.Meter("ganglion/<pkg>")`), so tests can install an in-memory
  reader.
- **The turn context.** The turn span wraps the handler in `core.Loop`,
  and the agent's logs use the turn context.
- **Tests:** per package, one test with `sdkmetric.NewManualReader` that
  checks the metric and its labels after the action, plus the agent's
  "no text in spans" check: span attributes never contain the message.

### Task 3: node wiring, config, live check — done; the owner's live look at the dashboard is pending

- `GANGLION_METRICS_ADDR` (default `127.0.0.1:9464`; `off`).
- `node.Run` sets telemetry up first and shuts it down last, and wraps the
  logger with `LogHandler`.
- **Live check:** `make live-agent`, then open
  `http://127.0.0.1:9464/webmetrics/` and see the turn and model metrics
  move. `make memory` records the new size.
- **Docs:** code map, component diagram, "Where we are".

As built:
- **Wiring.** `cmd/ganglion` sets telemetry up after reading the config
  and wraps the logger. A busy metrics port logs "telemetry: metrics are
  off" and the node runs on; watching must never stop it. Test binaries
  use `GANGLION_METRICS_ADDR=127.0.0.1:0`.
- **Measured 2026-11-02** (`make memory`):
  - binary 32.8 MB (18.3 before);
  - the agent 26.7 MB idle and 34.6 MB after 1,000 turns (17.6 and 25.6
    before).

### Task 4: the gaps (R-OBS-8, owner, 2026-11-03)

The rule is now that everything significant is a metric. These parts
log or journal what they do, but `/metrics` does not show it yet:

| Name | Type | Labels | What it answers |
|---|---|---|---|
| `ganglion_model_context_tokens` | histogram | `provider`, `model` | Input tokens per call: how big the context gets |
| `ganglion_turn_steps` | histogram | `outcome` | Model calls per turn |
| `ganglion_turn_fallbacks_total` | counter | `reason` (error kind) | Turns answered by a fallback model |
| `ganglion_tool_calls_total` | counter | `tool`, `decision` (allowed, confirm, denied, forbidden, expired, error) | What tools run and what the policy says |
| `ganglion_tool_call_duration_seconds` | histogram | `tool` | Slow tools |
| `ganglion_confirmations_total` | counter | `tool`, `answer` (approved, declined, expired) | How the owner answers |
| `ganglion_confirmations_pending` | gauge | — | Questions waiting now |
| `ganglion_module_calls_total` | counter | `module`, `result` (ok, trap, timeout, out_of_memory) | WASM module health |
| `ganglion_module_call_duration_seconds` | histogram | `module` | Module speed |
| `ganglion_web_fetch_bytes` | histogram | `result` | Page sizes, refusals by size |
| `ganglion_guard_verdicts_total` | counter | `verdict` (fact, instruction, error) | What the clean-context guard holds back |
| `ganglion_schedule_runs_total` | counter | `kind`, `outcome` | Reminders, tasks and system jobs that ran |
| `ganglion_schedule_lag_seconds` | histogram | `kind` | How late a job runs |
| `ganglion_inbox_pending`, `ganglion_outbox_pending` | gauge | — | Queue depth |
| `ganglion_sessions_total` | counter | `reason` (idle, new, resume, compacted) | Session churn and compaction |

- [ ] Add these, each in its package's `telemetry.go`, with a test per
  package. Names and labels may change while building; R-OBS-8 holds.
- [ ] `make memory` after, to record what they cost.
- [ ] The owner looks at the dashboard on a live run.
