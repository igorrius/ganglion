package skills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/igorrius/ganglion/router"
)

// writeSkill makes workspace/skills/<name>/SKILL.md and, when script is
// not empty, an executable run.sh with that body.
func writeSkill(t *testing.T, ws, name, front, script string) string {
	t.Helper()
	dir := filepath.Join(ws, "skills", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\n"+front+"---\nHow to do it.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if script != "" {
		if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var builtinFS = fstest.MapFS{
	"builtin/metrics/SKILL.md": {Data: []byte("---\nname: metrics\ndescription: Read the node's metrics\n---\nCall metrics.show.\n")},
}

func reasons(ps []Problem) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Skill+": "+p.Reason)
	}
	return strings.Join(out, "\n")
}

func TestLoadBuiltinAndWorkspace(t *testing.T) {
	ws := t.TempDir()
	writeSkill(t, ws, "disk-report", "name: disk-report\ndescription: Sizes of folders\nmodel:\n  tier: smart\n  context: long\n"+
		"script:\n  run: run.sh\n  effects: read\n  env: [LANG]\n  timeout: 20s\n", "echo hi\n")
	set, problems, err := Load(builtinFS, ws, nil)
	if err != nil || len(problems) != 0 {
		t.Fatalf("Load: %v\n%s", err, reasons(problems))
	}
	m, ok := set.Get("metrics")
	if !ok || !m.Builtin || m.Script != nil || !strings.Contains(m.Body, "metrics.show") {
		t.Fatalf("metrics %+v", m)
	}
	d, ok := set.Get("disk-report")
	if !ok || d.Model.Tier != router.TierSmart || d.Model.Context != "long" || d.Script == nil ||
		d.Script.Effects != EffectsRead || d.Script.Timeout != 20*time.Second || !slices.Equal(d.Script.Env, []string{"LANG"}) {
		t.Fatalf("disk-report %+v %+v", d, d.Script)
	}
	if d.ToolName() != "skill.disk_report" {
		t.Fatalf("tool name %s", d.ToolName())
	}
	if idx := set.Index(); idx != "- disk-report: Sizes of folders\n- metrics: Read the node's metrics" {
		t.Fatalf("index %q", idx)
	}
}

// A workspace skill cannot replace a built-in one silently: it is not
// loaded, and the problem names it.
func TestBuiltinCannotBeReplaced(t *testing.T) {
	ws := t.TempDir()
	writeSkill(t, ws, "metrics", "name: metrics\ndescription: mine\n", "")
	set, problems, err := Load(builtinFS, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := set.Get("metrics"); !m.Builtin || len(problems) != 1 || !strings.Contains(problems[0].Reason, "built-in") {
		t.Fatalf("metrics %+v, problems %s", m, reasons(problems))
	}
}

func TestBadSkillsAreNotLoaded(t *testing.T) {
	ws := t.TempDir()
	writeSkill(t, ws, "Bad_Name", "name: Bad_Name\ndescription: x\n", "")
	writeSkill(t, ws, "other-name", "name: not-the-dir\ndescription: x\n", "")
	writeSkill(t, ws, "no-desc", "name: no-desc\n", "")
	writeSkill(t, ws, "unknown", "name: unknown\ndescription: x\nfoo: 1\n", "")
	writeSkill(t, ws, "secret-env", "name: secret-env\ndescription: x\nscript:\n  run: run.sh\n  effects: read\n  env: [TELEGRAM_BOT_TOKEN]\n", "echo\n")
	writeSkill(t, ws, "key-env", "name: key-env\ndescription: x\nscript:\n  run: run.sh\n  effects: read\n  env: [OR_KEY]\n", "echo\n")
	writeSkill(t, ws, "path-env", "name: path-env\ndescription: x\nscript:\n  run: run.sh\n  effects: read\n  env: [PATH]\n", "echo\n")
	writeSkill(t, ws, "slow", "name: slow\ndescription: x\nscript:\n  run: run.sh\n  effects: read\n  timeout: 5m\n", "echo\n")
	writeSkill(t, ws, "no-effects", "name: no-effects\ndescription: x\nscript:\n  run: run.sh\n", "echo\n")
	writeSkill(t, ws, "outside", "name: outside\ndescription: x\nscript:\n  run: ../escape.sh\n  effects: read\n", "")
	dir := writeSkill(t, ws, "symlink", "name: symlink\ndescription: x\nscript:\n  run: link.sh\n  effects: read\n", "")
	if err := os.Symlink("/bin/sh", filepath.Join(dir, "link.sh")); err != nil {
		t.Fatal(err)
	}
	dir = writeSkill(t, ws, "not-exec", "name: not-exec\ndescription: x\nscript:\n  run: run.sh\n  effects: read\n", "echo\n")
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, problems, err := Load(builtinFS, ws, []string{"OR_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Names(); !slices.Equal(got, []string{"metrics"}) {
		t.Fatalf("loaded %v\n%s", got, reasons(problems))
	}
	if len(problems) != 12 {
		t.Fatalf("%d problems:\n%s", len(problems), reasons(problems))
	}
}

func TestNoWorkspaceSkills(t *testing.T) {
	set, problems, err := Load(builtinFS, filepath.Join(t.TempDir(), "missing"), nil)
	if err != nil || len(problems) != 0 || !slices.Equal(set.Names(), []string{"metrics"}) {
		t.Fatalf("%v %v %v", set.Names(), problems, err)
	}
}

// The embedded built-in skills load clean.
func TestEmbeddedBuiltinsLoad(t *testing.T) {
	set, problems, err := Load(Builtin, t.TempDir(), nil)
	if err != nil || len(problems) != 0 {
		t.Fatalf("%v\n%s", err, reasons(problems))
	}
	if _, ok := set.Get("metrics"); !ok {
		t.Fatal("no metrics skill")
	}
}

// Review finding 1: the default workspace is relative; a script is run
// by its absolute path.
func TestRelativeWorkspaceScriptRuns(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "probe", "name: probe\ndescription: x\nscript:\n  run: run.sh\n  effects: read\n", "echo ok\n")
	t.Chdir(filepath.Dir(root))
	set, problems, err := Load(builtinFS, filepath.Base(root), nil)
	if err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", err, problems)
	}
	sk, _ := set.Get("probe")
	if !filepath.IsAbs(sk.Dir) {
		t.Fatalf("dir %q is relative", sk.Dir)
	}
}

// Review finding 3: the model block's values are checked; a typo in
// privacy must not fail open.
func TestModelBlockValidated(t *testing.T) {
	ws := t.TempDir()
	writeSkill(t, ws, "typo-privacy", "name: typo-privacy\ndescription: x\nmodel:\n  privacy: local\n", "")
	writeSkill(t, ws, "typo-tier", "name: typo-tier\ndescription: x\nmodel:\n  tier: huge\n", "")
	writeSkill(t, ws, "typo-context", "name: typo-context\ndescription: x\nmodel:\n  context: medium\n", "")
	writeSkill(t, ws, "read", "name: read\ndescription: x\n", "") // its tool would be skill.read
	writeSkill(t, ws, "good", "name: good\ndescription: |\n  two\n  lines\nmodel:\n  tier: smart\n  context: long\n  privacy: local-only\n", "")
	set, problems, err := Load(builtinFS, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Names(); !slices.Equal(got, []string{"good", "metrics"}) || len(problems) != 4 {
		t.Fatalf("loaded %v\n%s", got, reasons(problems))
	}
	if g, _ := set.Get("good"); g.Description != "two lines" {
		t.Fatalf("description %q", g.Description)
	}
}

// A file saved on Windows (CRLF, a BOM) loads.
func TestCRLFAndBOM(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, "skills", "win")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	src := "\ufeff---\r\nname: win\r\ndescription: from Windows\r\n---\r\nSteps.\r\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	set, problems, err := Load(builtinFS, ws, nil)
	if _, ok := set.Get("win"); err != nil || !ok {
		t.Fatalf("%v %s", err, reasons(problems))
	}
}

// A script or a skill directory others can write is refused.
func TestWritableByOthersRefused(t *testing.T) {
	ws := t.TempDir()
	dir := writeSkill(t, ws, "loose", "name: loose\ndescription: x\nscript:\n  run: run.sh\n  effects: read\n", "echo\n")
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o777); err != nil {
		t.Fatal(err)
	}
	if _, problems, _ := Load(builtinFS, ws, nil); len(problems) != 1 || !strings.Contains(problems[0].Reason, "writable") {
		t.Fatalf("problems %s", reasons(problems))
	}
}
