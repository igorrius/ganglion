package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/tool"
)

type fakeStats struct{}

func (fakeStats) Read(context.Context) host.Report {
	return host.Report{Machine: host.Machine{MemTotal: 1 << 30, MemAvailable: 1 << 29, OK: true}}
}

func TestHostStatsSpec(t *testing.T) {
	s := NewHostStats(fakeStats{}).Spec()
	if s.Name != HostStatsTool || s.Class != tool.ClassRead || s.Policy != tool.PolicyAuto || !s.AdminOnly || !s.LocalSafe {
		t.Fatalf("spec %+v", s)
	}
}

func TestHostStatsRunIsNotExternal(t *testing.T) {
	h := NewHostStats(fakeStats{})
	res, err := h.Run(t.Context(), tool.Call{Name: HostStatsTool, Args: json.RawMessage(`{}`)})
	if err != nil || res.External || !strings.Contains(res.Text, "Memory:") {
		t.Fatalf("result %+v, %v", res, err)
	}
}

func TestHostStatsTakesNoArguments(t *testing.T) {
	s := NewHostStats(fakeStats{}).Spec()
	if err := tool.CheckArgs(s, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("an empty call: %v", err)
	}
	if err := tool.CheckArgs(s, json.RawMessage(`{"path":"/"}`)); !errors.Is(err, tool.ErrBadArgs) {
		t.Fatalf("a call with an argument: %v, want ErrBadArgs", err)
	}
}
