// Package buckets holds histogram buckets by base unit, passed when an
// instrument is made (R-OBS-8). The SDK's default buckets (0, 5, 10, 25,
// … 10000) suit milliseconds, not seconds, bytes or tokens: every
// sub-second time would land in one bucket. A leaf, so an instrumented
// package does not import the node's telemetry setup.
package buckets

import "go.opentelemetry.io/otel/metric"

var (
	// Seconds is for durations: 5 ms to 5 minutes (a turn through the
	// claude CLI takes minutes).
	Seconds = metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300)
	// Bytes is for sizes: 1 KiB to 16 MiB, by fours.
	Bytes = metric.WithExplicitBucketBoundaries(1<<10, 1<<12, 1<<14, 1<<16, 1<<18, 1<<20, 1<<22, 1<<24)
	// Tokens is for a model's context: 500 to 256 k tokens.
	Tokens = metric.WithExplicitBucketBoundaries(500, 1000, 2000, 4000, 8000, 16000, 32000, 64000, 128000, 256000)
	// Counts is for small counts, such as model calls in a turn.
	Counts = metric.WithExplicitBucketBoundaries(1, 2, 3, 4, 6, 8, 12, 16, 24, 32)
)
