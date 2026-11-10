package runner

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/internal/size"
)

const deyeManifest = `
name: deye-reader
version: 1
triggers:
  - cron: "*/30 * * * *"
  - tool: status
    about: Battery, grid and load now.
    output: {soc: integer, grid: boolean}
capabilities:
  http.fetch: [eu1-developer.deyecloud.com]
  secret.get: [DEYE_APP_ID, DEYE_APP_SECRET]
  event.emit: [battery.changed]
  state: true
config:
  inverter_sn: {type: string, required: true}
  poll_s: {type: number, default: "30"}
cells:
  report:
    soc: {kind: sensor, type: integer, about: "Battery charge, percent"}
    grid: {kind: sensor, type: "boolean?", about: Grid is up}
  read: [house.power]
limits:
  memory: 8MiB
  timeout: 20s
escalate:
  - on: module.exception
  - on: auth_failed
`

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest([]byte(deyeManifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Name != "deye-reader" || m.Version != 1 {
		t.Errorf("name/version = %s/%d", m.Name, m.Version)
	}
	if len(m.Triggers) != 2 || m.Triggers[0].Cron != "*/30 * * * *" || m.Triggers[1].Tool != "status" {
		t.Errorf("triggers = %+v", m.Triggers)
	}
	c := m.Capabilities
	if !slices.Equal(c.HTTPFetch, []string{"eu1-developer.deyecloud.com"}) || !slices.Equal(c.SecretGet, []string{"DEYE_APP_ID", "DEYE_APP_SECRET"}) || !c.State {
		t.Errorf("capabilities = %+v", c)
	}
	if k := m.Config["inverter_sn"]; k.Type != "string" || !k.Required {
		t.Errorf("config = %v", m.Config)
	}
	if k := m.Config["poll_s"]; k.Type != "number" || k.Required || k.Default != "30" {
		t.Errorf("config = %v", m.Config)
	}
	if tr := m.Triggers[1]; tr.About == "" || tr.Output == nil || tr.Output.Type != "object" || tr.Output.Fields["soc"].Type != "integer" {
		t.Errorf("tool trigger = %+v", tr)
	}
	soc, grid := m.Cells.Report["soc"], m.Cells.Report["grid"]
	if soc.Kind != "sensor" || soc.Type.Type != "integer" || soc.About != "Battery charge, percent" || !grid.Type.Nullable {
		t.Errorf("cells = %+v", m.Cells)
	}
	if !slices.Equal(m.Cells.Read, []string{"house.power"}) {
		t.Errorf("cells.read = %v", m.Cells.Read)
	}
	if m.Limits.Memory != 8<<20 || m.Limits.Timeout != 20*time.Second {
		t.Errorf("limits = %+v, want 8 MiB and 20 s", m.Limits)
	}
	if len(m.Escalate) != 2 || m.Escalate[1].On != "auth_failed" {
		t.Errorf("escalate = %+v", m.Escalate)
	}
}

func TestParseManifestDefaultsLimits(t *testing.T) {
	m, err := ParseManifest([]byte("name: tiny\nversion: 1\n"))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Limits.Memory != DefaultMemory || m.Limits.Timeout != DefaultTimeout {
		t.Errorf("limits = %+v, want defaults", m.Limits)
	}
	if m.Capabilities.State || len(m.Capabilities.HTTPFetch) != 0 || len(m.Capabilities.SecretGet) != 0 {
		t.Errorf("capabilities = %+v, want nothing granted by default", m.Capabilities)
	}
}

func TestParseManifestRejects(t *testing.T) {
	cases := map[string]string{
		"no name":         "version: 1\n",
		"bad memory":      "name: x\nlimits:\n  memory: lots\n",
		"bad timeout":     "name: x\nlimits:\n  timeout: forever\n",
		"unknown field":   "name: x\ncapabilities:\n  shell.exec: true\n",
		"memory too big":  "name: x\nlimits:\n  memory: 8GiB\n",
		"unknown trigger": "name: x\ntriggers:\n  - webhook: /x\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest([]byte(src)); err == nil {
				t.Fatalf("ParseManifest accepted:\n%s", src)
			}
		})
	}
}

func TestParseManifestRejectsV2(t *testing.T) {
	cases := map[string]struct{ src, field string }{
		"cell kind":   {"cells:\n  report:\n    soc: {kind: switch, type: integer}\n", "soc"},
		"cell name":   {"cells:\n  report:\n    So-C!: {kind: sensor, type: integer}\n", "So-C!"},
		"cell type":   {"cells:\n  report:\n    soc: {kind: sensor}\n", "soc"},
		"read id":     {"cells:\n  read: [soc]\n", "soc"},
		"config type": {"config:\n  day: {type: date}\n", "day"},
		"tool name":   {"triggers:\n  - tool: Status\n", "Status"},
		"tool twice":  {"triggers:\n  - tool: status\n    about: one\n  - tool: status\n    about: other\n", "status is declared twice"},
		"cron twice":  {"triggers:\n  - cron: \"*/30 * * * *\"\n  - cron: \"*/30  *  * * *\"\n", "repeats the cron line"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte("name: x\n" + tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("err = %v, want one naming %q", err, tc.field)
			}
		})
	}
}

func TestParseMemorySizes(t *testing.T) {
	for in, want := range map[string]uint64{"64KiB": 64 << 10, "8MiB": 8 << 20, "1GiB": 1 << 30, "65536": 65536} {
		got, err := size.Parse(in)
		if err != nil || got != want {
			t.Errorf("size.Parse(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := size.Parse(strings.Repeat("9", 30) + "MiB"); err == nil {
		t.Error("size.Parse accepted an overflowing value")
	}
}

func TestParseManifestCellTriggerAndNotify(t *testing.T) {
	src := "name: x\ncapabilities:\n  notify: {per_day: 5}\ncells:\n  read: [p.soc, p.grid]\ntriggers:\n  - cell: [p.soc]\n"
	m, err := ParseManifest([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Listens(); !slices.Equal(got, []string{"p.soc"}) {
		t.Fatalf("Listens = %v", got)
	}
	if m.Capabilities.Notify == nil || m.Capabilities.Notify.PerDay != 5 {
		t.Fatalf("notify = %+v", m.Capabilities.Notify)
	}
	if got := (Manifest{}).Listens(); got != nil {
		t.Fatalf("Listens of none = %v", got)
	}
}

func TestParseManifestRejectsCellTriggerAndNotify(t *testing.T) {
	cases := map[string]struct{ src, want string }{
		"not read":   {"triggers:\n  - cell: [p.soc]\n", `trigger 0: cell "p.soc" is not in cells.read`},
		"own cell":   {"cells:\n  read: [x.a]\ntriggers:\n  - cell: [x.a]\n", `trigger 0: a module cannot listen to its own cell "x.a"`},
		"two kinds":  {"cells:\n  read: [p.a]\ntriggers:\n  - cron: \"* * * * *\"\n    cell: [p.a]\n", "exactly one of cron, tool, cell"},
		"tool+cell":  {"cells:\n  read: [p.a]\ntriggers:\n  - tool: t\n    cell: [p.a]\n", "exactly one of cron, tool, cell"},
		"none":       {"triggers:\n  - about: x\n", "exactly one of cron, tool, cell"},
		"second":     {"cells:\n  read: [p.a, p.b]\ntriggers:\n  - cell: [p.a]\n  - cell: [p.b]\n", "one cell trigger"},
		"notify 0":   {"capabilities:\n  notify: {per_day: 0}\n", "notify.per_day must be 1..100"},
		"notify 101": {"capabilities:\n  notify: {per_day: 101}\n", "notify.per_day must be 1..100"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte("name: x\n" + tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}
