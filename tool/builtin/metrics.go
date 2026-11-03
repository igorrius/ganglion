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
	"github.com/igorrius/ganglion/telemetry"
	"github.com/igorrius/ganglion/tool"
)

// MetricsTool is the tool that shows the node's metrics; /metrics runs it
// without a model (ruling M2).
const MetricsTool = "metrics.show"

// MetricSelection is what metrics.show draws (telemetry's type).
type MetricSelection = telemetry.Selection

// MetricSection is one of the dashboard's sections (telemetry's type).
type MetricSection = telemetry.Section

// MetricsRenderer draws the node's own metrics (telemetry.Renderer).
type MetricsRenderer interface {
	Picture(ctx context.Context, w io.Writer, sel MetricSelection) error
	Summary(ctx context.Context, w io.Writer, sel MetricSelection, notable bool) error
	Sections(ctx context.Context) ([]MetricSection, error)
}

// Limits of metrics.show.
const (
	maxGlobs       = 8
	maxSummary     = 4000 // runes of the summary the model reads
	captionNotable = 5    // lines of the picture's caption
)

var (
	metricGlob   = regexp.MustCompile(`^[a-z_*:]{1,64}$`)
	metricFilter = regexp.MustCompile(`^[a-z0-9_*:]{1,64}$`)
)

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
			"of what stands out. choice: all (with what is hidden by default), a group (ganglion, runtime, process) or a text filter " +
			"over the names; or metrics: name globs such as ganglion_turns*. Nothing set draws the default dashboard.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"choice":{"type":"string","description":"all, a group or a text filter"},` +
			`"metrics":{"type":"array","items":{"type":"string"},"description":"name globs, at most 8"}},"additionalProperties":false}`),
		Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true}
}

// Run draws the picture and the summary.
func (m *MetricsShow) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a struct {
		Choice  string   `json:"choice"`
		Metrics []string `json:"metrics"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tool.Result{}, errors.New(`metrics.show: arguments: want {"choice": "runtime"} or {"metrics": ["ganglion_*"]}`)
	}
	if len(a.Metrics) > maxGlobs || slices.ContainsFunc(a.Metrics, func(g string) bool { return !metricGlob.MatchString(g) }) {
		return tool.Result{}, fmt.Errorf("metrics.show: at most %d globs of lower-case letters, _, : and *", maxGlobs)
	}
	sel, err := m.choose(ctx, strings.TrimSpace(a.Choice))
	if err != nil {
		return tool.Result{}, err
	}
	sel.Globs = append(sel.Globs, a.Metrics...)
	var png, sum bytes.Buffer
	if err := m.r.Picture(ctx, &png, sel); err != nil {
		return tool.Result{}, fmt.Errorf("metrics.show: %w", err)
	}
	if err := m.r.Summary(ctx, &sum, sel, true); err != nil {
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

// groupAliases name a group by a word people use for it.
var groupAliases = map[string]string{"runtime": "go"}

// choose reads a choice: "all", a group (a section's key, title, a word
// of its title, or an alias), or a text filter over the names (a
// substring, or globs when it has a *).
func (m *MetricsShow) choose(ctx context.Context, choice string) (MetricSelection, error) {
	low := strings.ToLower(choice)
	switch low {
	case "":
		return MetricSelection{}, nil
	case "all":
		return MetricSelection{All: true}, nil
	}
	if key, ok := groupAliases[low]; ok {
		return MetricSelection{Sections: []string{key}}, nil
	}
	secs, err := m.r.Sections(ctx)
	if err != nil {
		return MetricSelection{}, fmt.Errorf("metrics.show: %w", err)
	}
	for _, s := range secs {
		if strings.EqualFold(s.Key, low) || strings.EqualFold(s.Title, low) || slices.ContainsFunc(strings.Fields(s.Title),
			func(w string) bool { return strings.EqualFold(w, low) }) {
			return MetricSelection{Sections: []string{s.Key}}, nil
		}
	}
	if !metricFilter.MatchString(low) {
		return MetricSelection{}, errors.New("metrics.show: choice: all, a group, or a filter of lower-case letters, digits, _, : and *")
	}
	if !strings.Contains(low, "*") {
		low = "*" + low + "*"
	}
	return MetricSelection{Globs: []string{low}}, nil
}
