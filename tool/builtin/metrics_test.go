package builtin_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

type fakeRenderer struct{ globs []string }

func (f *fakeRenderer) Picture(_ context.Context, w io.Writer, globs []string) error {
	f.globs = globs
	_, err := io.WriteString(w, "\x89PNG\r\n\x1a\nfake")
	return err
}

func (f *fakeRenderer) Summary(_ context.Context, w io.Writer, _ []string, _ bool) error {
	_, err := io.WriteString(w, `{"schema":"promtop.summary/v1","notable":[`+
		`{"metric":"ganglion_turns_total","labels":{"outcome":"error"},"reason":"errors rising"},`+
		`{"metric":"ganglion_outbox_pending","reason":"not draining"}]}`)
	return err
}

func TestMetricsShowSendsPicture(t *testing.T) {
	r := &fakeRenderer{}
	res, err := builtin.NewMetricsShow(r).Run(context.Background(), call("metrics.show", `{"metrics":["ganglion_*"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Out) != 1 || res.Out[0].Attach == nil || res.Out[0].Attach.Kind != event.AttachPhoto ||
		!strings.HasPrefix(string(res.Out[0].Attach.Data), "\x89PNG") || res.Out[0].Conv != "telegram:42" || res.Out[0].IdemKey != "E1:2:metrics" {
		t.Fatalf("out %+v", res.Out)
	}
	caption := res.Out[0].Text
	if !strings.Contains(caption, "ganglion_turns_total{outcome=error}: errors rising") || !strings.Contains(caption, "ganglion_outbox_pending: not draining") {
		t.Fatalf("caption %q", caption)
	}
	if !strings.Contains(res.Text, "promtop.summary/v1") || res.External || len(r.globs) != 1 {
		t.Fatalf("result %+v, globs %v", res, r.globs)
	}
	if s := builtin.NewMetricsShow(r).Spec(); s.Class != tool.ClassRead || !s.AdminOnly {
		t.Fatalf("spec %+v", s)
	}
}

func TestMetricsShowRejectsBadArgs(t *testing.T) {
	m := builtin.NewMetricsShow(&fakeRenderer{})
	for _, args := range []string{`{"metrics":["a/b"]}`, `{"metrics":["a","b","c","d","e","f","g","h","i"]}`, `{"metrics":["` + strings.Repeat("a", 65) + `"]}`} {
		if _, err := m.Run(context.Background(), call("metrics.show", args)); err == nil {
			t.Errorf("%s accepted", args)
		}
	}
}
