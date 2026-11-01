# Home internet during outages and DeyeCloud polling limits

- **Date:** 2026-11-01
- **Task:** P0 · 0.7 ([spec 10.1](../spec.md#101-p0--recon--tasks-and-acceptance))
- **Questions:** does the home internet stay up during a power cut, so the
  cloud path (DeyeCloud, Tuya Cloud) works when load shedding is needed? Do
  DeyeCloud limits allow polling every 5 minutes?

## Home internet during an outage

**Answer: yes, and it outlasts the inverter.** The router is powered by the
inverter and also has its own battery (owner, 2026-11-01).

Observed the same day during a real outage: the production bot read fresh
DeyeCloud samples while the grid was down (grid voltage 4.6–4.7 V,
collection times 18:27, 18:47 and 19:03 Kyiv time, each a few minutes old).
So the logger, the router and the provider's side all stayed online.

Consequences:

- The cloud path is usable for P1: the load-shedding flow can run from the
  cloud-hosted node and switch plugs through Tuya Cloud.
- Because the router outlives the inverter, the "no fresh data" alert can
  still be delivered after the inverter shuts down.
- Remaining risk, not measured: in a long outage the provider's own backup
  may run out. The flow already treats data older than 20 minutes as "do
  nothing and report" (spec 8.2), which is the safe failure mode.
- A home runner with LAN access (P2) stays valuable for independence from
  the cloud, but is no longer a prerequisite for the first flow.

## DeyeCloud polling limits

**Official quota: 100,000 calls a month, free** — shown in the owner's
DeyeCloud developer console on 2026-11-01. No per-minute limit is shown
there; whether the quota is per account or per app is not stated, so the
estimate below assumes the production bot and ganglion share it.

Secondary evidence: the Home Assistant integration `hass-deye-cloud`
([PR #1](https://github.com/maxbanton/hass-deye-cloud/pull/1), 2026-09-21)
says "Deye has announced usage tracking, quotas and rate limits for the
Cloud OpenAPI" and handles "HTTP 429 and rate-limit error messages" by
backing off, "honouring Retry-After, capped at 30 minutes". It cut its own
usage from about 8,600 to about 1,300 calls a day per inverter and notes
that "loggers only upload every 3 to 5 minutes anyway". It cites no official
announcement.

Our load: `/device/latest` every 30 minutes with grid (48 calls a day) and
every 5 minutes without (at most 288 a day), plus a login about every two
months (the token lives about 60 days).

| Case | Calls a month | Share of the quota |
|---|---|---|
| Grid all month, ganglion only | ~1,500 | 1.5% |
| Outage all month, ganglion only | ~8,900 | 8.9% |
| Outage all month, ganglion + the production bot (every 30 min) | ~10,400 | 10.4% |
| Development and tests, generous | ~2,000 | 2% |

Even the worst case uses about an eighth of the quota, and polling faster
than 5 minutes would not give fresher data.

Consequences:

- Polling every 5 minutes during outages stays in the spec.
- New requirement: on HTTP 429 or a rate-limit error, the Deye reader backs
  off, honouring `Retry-After`, instead of retrying on the next tick.
