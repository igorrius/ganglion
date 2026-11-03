# ADR-0045: Render the metrics picture in process with promtop, and keep attachments in the outbox row

- **Status:** Proposed
- **Date:** 2026-11-03
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-9), [7.9](../spec.md#79-observability) (R-OBS-8); design section 6.3 ([M3b and M3c design](../plans/p1-m3b-m3c-design.md)); [ADR-0026](0026-opentelemetry-prometheus-and-promtop.md), [ADR-0031](0031-agent-security-baseline.md); plan tasks 3 and 4 ([M3c plan](../plans/p1-m3c-modules-skills-diagnostics.md))

## Context

The owner asked the bot for its dashboard (2026-11-03). The agent
correctly refused, because `web.fetch` never reaches loopback (spec
principle 9). The dashboard at `127.0.0.1:9464/webmetrics/` cannot be
reached from a phone, and Telegram is where the owner is.

Task 3.16 asks for "Show me the agent's metrics" → a promtop picture.
That needs two decisions:
1. **How to draw.** promtop v0.3.2 renders PNG and its `summary` JSON
   only in its command line. The renderer is in `internal/`; the public
   package is `dash`, the web dashboard ganglion already embeds. That
   dashboard already reads the in-process metrics handler every 2 s and
   keeps history.
2. **How a picture reaches the chat with the delivery guarantee
   (R-DLV-9).** Today the outbox stores an `Outgoing` as JSON text in a
   row.

## Options

Drawing:

- **A. In process: a public `dash.(*Dashboard).Render` in promtop
  v0.4.0.** It draws the dashboard's own history as PNG or a summary,
  with no network.
- **B. The promtop binary as a skill script.** It runs
  `promtop render 127.0.0.1:9464`.
- **C. Our own renderer.**

Storage:

- **D. Bytes in the outbox row.** One attachment per message, at most
  4 MiB, base64 in the JSON body.
- **E. Files beside the database.** The row holds a path.

## Comparison

| Criterion | A. In process | B. Binary | C. Own |
|---|---|---|---|
| Needs `/metrics` listening | No | Yes | No |
| Extra binary in the image and on a Pi | No | Yes | No |
| Rates and charts over time | The dashboard's history | One scrape, or a state file | To build |
| Cost | A promtop release; `golang.org/x/image` and fonts linked (size measured in plan task 4, budget +3 MB) | A subprocess per picture | Weeks |

| Criterion | D. In the row | E. Files |
|---|---|---|
| Atomic with the message | Yes, one transaction | Two steps; a crash can orphan a file or lose one |
| Crash and retry | As text today | A cleanup job |
| Memory | Up to 4 MiB × 4/3 per row, loaded at send time | Streamed |
| Fits the first use | A promtop PNG is a few hundred KB | Over-built |

## Decision

- **A** for drawing (owner, 2026-11-03). promtop is the owner's own
  library, so v0.4.0 is taken at once, an exception to the two-week
  rule. The dashboard exists even with `--metrics-addr off`: it reads
  the in-process handler, and nothing listens. `metrics.show` (for the
  agent) and `/metrics` (no model, admins only) both use it.
- **D** for storage. The delivery guarantee stays one transaction. The
  cap keeps a row bounded. The outbox's resumable parts send a long text
  after the attachment.

## Security

- **Trust boundaries.** None new. The picture is the node's own data;
  the model gets the summary, not raw exposition.
- **What an attacker controls.** Nothing in the picture: metric labels
  are bounded sets the code sets (R-OBS-8), never ids or outside text.
- **Abuse cases:**
  - *The model is talked into reading loopback.* No path exists; the
    picture comes from the registry, and `web.fetch` still refuses
    loopback.
  - *A family member asks for metrics.* The tool and the command are
    for admins only.
  - *An oversize attachment fills the database.* It is refused before
    the outbox.

## Consequences

- The owner sees the node's health from the phone, even when no model
  answers.
- The binary grows by promtop's renderer. If that is over the budget,
  promtop moves its snapshot type out of the package that pulls in its
  terminal UI.
- Pictures from other tools (M4: inverter charts) use the same
  attachment path.

## Revisit when

- Attachments are larger than a few MB, or many are sent at once.
- A second channel renders captions differently.
