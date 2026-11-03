package agent

import (
	"context"
	"io"
	"testing"

	"github.com/igorrius/ganglion/tool/builtin"
)

type pictureOnly struct{}

func (pictureOnly) Picture(_ context.Context, w io.Writer, _ []string) error {
	_, err := io.WriteString(w, "\x89PNG")
	return err
}

func (pictureOnly) Summary(_ context.Context, w io.Writer, _ []string, _ bool) error {
	_, err := io.WriteString(w, `{"schema":"promtop.summary/v1","notable":[]}`)
	return err
}

// M2: /metrics sends the picture without a model, even when no model
// would answer.
func TestMetricsCommandNoModel(t *testing.T) {
	e := newConfirmEnv(t, builtin.NewMetricsShow(pictureOnly{}))
	out := e.turn(t, message(owner, "Igor", "/metrics"))
	if len(e.smart.requests()) != 0 || len(out) != 1 || out[0].Attach == nil || out[0].Text != "Nothing stands out." {
		t.Fatalf("out %+v", out)
	}
}

func TestMetricsCommandFamilyRefused(t *testing.T) {
	e := newConfirmEnv(t, builtin.NewMetricsShow(pictureOnly{}))
	e.admin = false
	if out := e.turn(t, message(owner, "Igor", "/metrics")); len(out) != 1 || out[0].Attach != nil || out[0].Text != replyTasksNotAvailable {
		t.Fatalf("out %+v", out)
	}
}

type brokenRenderer struct{ pictureOnly }

func (brokenRenderer) Picture(context.Context, io.Writer, []string) error { return io.ErrUnexpectedEOF }

func TestMetricsCommandSaysWhenItCannotDraw(t *testing.T) {
	e := newConfirmEnv(t, builtin.NewMetricsShow(brokenRenderer{}))
	if out := e.turn(t, message(owner, "Igor", "/metrics")); len(out) != 1 || out[0].Text != replyNoMetrics {
		t.Fatalf("out %+v", out)
	}
}
