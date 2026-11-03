---
name: metrics
description: Show or explain the node's own metrics (speed, errors, model calls, costs, queues)
---
When the owner asks how the agent or the node is doing, or for its metrics:

1. Call `metrics.show`. It sends the owner a picture of the dashboard and
   returns a short summary of what stands out (the `promtop.summary/v1`
   document), so you need not describe the picture.
2. Read the summary's `notable` list: each line names a metric and why it
   stands out (errors rising, a queue not draining, a slow model, a cost).
   Explain those in a few plain sentences. Labels such as `tool`,
   `result` or `provider` say which one.
3. If nothing is notable, say the node looks healthy.

Never fetch the node's own address with `web.fetch`: it is refused, and
`metrics.show` reads the metrics inside the process.
