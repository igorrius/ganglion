package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/tool"
)

// MetricsTool is the tool that shows the node's metrics; /metrics runs it
// without a model (ruling M2).
const MetricsTool = "metrics.show"

// MetricsRenderer draws the node's own metrics (telemetry.Renderer).
type MetricsRenderer interface {
	Picture(ctx context.Context, w io.Writer, globs []string) error
	Summary(ctx context.Context, w io.Writer, globs []string, notable bool) error
}

// Limits of metrics.show.
const (
	maxGlobs       = 8
	maxSummary     = 4000 // runes of the summary the model reads
	captionNotable = 5    // lines of the picture's caption
)

var metricGlob = regexp.MustCompile(`^[a-z_*:]{1,64}$`)

// MetricsShow sends the owner a picture of the node's metrics and gives
// the model the notable summary (3.16, ADR-0045). The node's own data:
// labels are bounded sets the code sets, so it is not outside content.
type MetricsShow struct{ r MetricsRenderer }

// NewMetricsShow returns the tool.
func NewMetricsShow(r MetricsRenderer) *MetricsShow { return &MetricsShow{r: r} }

// Spec describes metrics.show.
func (*MetricsShow) Spec() tool.Spec {
	return tool.Spec{Name: MetricsTool,
		Description: "Send the owner a picture of the agent's own metrics (speed, errors, model calls, cost, queues) and read a summary " +
			"of what stands out. metrics: optional name globs such as ganglion_turns* to show only those.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"metrics":{"type":"array","items":{"type":"string"},` +
			`"description":"name globs, at most 8"}},"additionalProperties":false}`),
		Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true}
}

// Run draws the picture and the summary.
func (m *MetricsShow) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a struct {
		Metrics []string `json:"metrics"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tool.Result{}, errors.New(`metrics.show: arguments: want {"metrics": ["ganglion_*"]}`)
	}
	if len(a.Metrics) > maxGlobs || slices.ContainsFunc(a.Metrics, func(g string) bool { return !metricGlob.MatchString(g) }) {
		return tool.Result{}, fmt.Errorf("metrics.show: at most %d globs of lower-case letters, _, : and *", maxGlobs)
	}
	var png, sum bytes.Buffer
	if err := m.r.Picture(ctx, &png, a.Metrics); err != nil {
		return tool.Result{}, fmt.Errorf("metrics.show: %w", err)
	}
	if err := m.r.Summary(ctx, &sum, a.Metrics, true); err != nil {
		return tool.Result{}, fmt.Errorf("metrics.show: %w", err)
	}
	out := event.Outgoing{IdemKey: c.Key() + ":metrics", Transport: c.Transport, Conv: c.Conv, Text: caption(sum.Bytes()),
		Attach: &event.Attachment{Kind: event.AttachPhoto, Name: "metrics.png", Type: "image/png", Data: png.Bytes()}}
	return tool.Result{Text: text.Truncate(sum.String(), maxSummary), Out: []event.Outgoing{out}}, nil
}

// caption is the picture's text: the first notable lines, labels and
// reasons only.
func caption(summary []byte) string {
	var doc struct {
		Notable []struct {
			Metric string            `json:"metric"`
			Labels map[string]string `json:"labels"`
			Reason string            `json:"reason"`
		} `json:"notable"`
	}
	if json.Unmarshal(summary, &doc) != nil || len(doc.Notable) == 0 {
		return "Nothing stands out."
	}
	lines := []string{"Notable:"}
	for _, n := range doc.Notable[:min(len(doc.Notable), captionNotable)] {
		name := n.Metric
		if len(n.Labels) > 0 {
			keys := make([]string, 0, len(n.Labels))
			for k := range n.Labels {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			pairs := make([]string, len(keys))
			for i, k := range keys {
				pairs[i] = k + "=" + n.Labels[k]
			}
			name += "{" + strings.Join(pairs, ",") + "}"
		}
		lines = append(lines, "- "+name+": "+n.Reason)
	}
	return strings.Join(lines, "\n")
}
