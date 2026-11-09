//go:build unix

package skills

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/igorrius/ganglion/sandbox"
)

func scriptSkill(t *testing.T, body, env string, timeout time.Duration) Skill {
	t.Helper()
	ws := t.TempDir()
	front := "name: probe\ndescription: x\nscript:\n  run: run.sh\n  effects: read\n" + env
	writeSkill(t, ws, "probe", front, body)
	set, problems, err := Load(builtinFS, ws, nil)
	if err != nil || len(problems) != 0 {
		t.Fatalf("%v %s", err, reasons(problems))
	}
	sk, _ := set.Get("probe")
	if timeout > 0 {
		sk.Script.Timeout = timeout
	}
	return sk
}

// direct is a Starter that runs the command unconfined and records what it
// was asked for: these tests are about the copy and the environment, not the
// confinement (the abuse test in config has the real sandbox).
type direct struct {
	argv []string
	dir  string
	env  []string
}

func (d *direct) Command(ctx context.Context, argv []string, dir string, env ...string) (*exec.Cmd, error) {
	d.argv, d.dir, d.env = argv, dir, env
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// The sandbox's policy gives these; a skill's own entries replace them.
	cmd.Dir, cmd.Env = dir, append([]string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "TERM=dumb"}, env...)
	return cmd, nil
}

// offStarter is a sandbox that is off.
type offStarter struct{}

func (offStarter) Command(context.Context, []string, string, ...string) (*exec.Cmd, error) {
	return nil, sandbox.ErrOff
}

func runnerOn(lookup map[string]string, st Starter) *Runner {
	cfg := DefaultRunConfig()
	cfg.TZ = "Europe/Kyiv"
	cfg.Env = func(k string) (string, bool) { v, ok := lookup[k]; return v, ok }
	return NewRunner(cfg, st)
}

func runner(lookup map[string]string) *Runner { return runnerOn(lookup, &direct{}) }

// 3.26: a script runs from a copy of its skill in the work folder, with its
// files beside it and its mode kept; the skill's own folder is never used.
func TestAScriptRunsFromACopyOfItsSkill(t *testing.T) {
	sk := scriptSkill(t, "echo \"$(pwd)\"; cat \"$(dirname \"$0\")/data.txt\"\n", "", 0)
	if err := os.WriteFile(filepath.Join(sk.Dir, "data.txt"), []byte("beside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &direct{}
	out, err := runnerOn(nil, d).Run(context.Background(), sk, nil)
	if err != nil || out.Exit != 0 {
		t.Fatalf("out %+v, %v", out, err)
	}
	if want := filepath.Join(d.dir, "skill", "run.sh"); len(d.argv) != 1 || d.argv[0] != want {
		t.Fatalf("argv %q, want [%s]", d.argv, want)
	}
	if d.dir == sk.Dir || strings.HasPrefix(d.argv[0], sk.Dir) {
		t.Fatalf("the script ran in the skill's own folder: %q %q", d.dir, d.argv)
	}
	if lines := strings.Split(strings.TrimSpace(out.Stdout), "\n"); len(lines) != 2 || lines[0] != d.dir || lines[1] != "beside" {
		t.Fatalf("stdout %q, want the work folder and data.txt", out.Stdout)
	}
	if _, err := os.Stat(d.dir); err == nil {
		t.Fatal("the work folder was left behind")
	}
}

// 3.26: the script gets HOME, TZ and the names its skill lists, never more.
func TestAScriptGetsTheSandboxEnvironmentPlusItsOwn(t *testing.T) {
	sk := scriptSkill(t, "true\n", "  env: [KEEP, ABSENT]\n", 0)
	d := &direct{}
	_, err := runnerOn(map[string]string{"KEEP": "yes", "OTHER": "no", "TELEGRAM_BOT_TOKEN": "tok"}, d).Run(context.Background(), sk, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(d.env, "\n")
	for _, want := range []string{"HOME=" + d.dir, "TZ=Europe/Kyiv", "KEEP=yes"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %q", want, d.env)
		}
	}
	for _, bad := range []string{"OTHER", "TELEGRAM", "tok", "ABSENT", "PATH=", "LANG="} {
		if strings.Contains(got, bad) {
			t.Errorf("%q in %q", bad, d.env)
		}
	}
}

// 3.26, fail closed: with the sandbox off a script does not run.
func TestScriptsAreOffWithTheSandbox(t *testing.T) {
	_, err := runnerOn(nil, offStarter{}).Run(context.Background(), scriptSkill(t, "echo hi\n", "", 0), nil)
	if !errors.Is(err, ErrScriptsOff) {
		t.Fatalf("err = %v, want ErrScriptsOff", err)
	}
}

// A skill folder past the cap is not copied for every run.
func TestASkillFolderTooBigIsRefused(t *testing.T) {
	sk := scriptSkill(t, "true\n", "", 0)
	if err := os.WriteFile(filepath.Join(sk.Dir, "big.bin"), make([]byte, maxSkillBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &direct{}
	if _, err := runnerOn(nil, d).Run(context.Background(), sk, nil); !errors.Is(err, errSkillTooBig) || d.argv != nil {
		t.Fatalf("err %v, started %q", err, d.argv)
	}
}

// 3.7 done when: a skill with a script runs, its input on stdin.
func TestScriptSkillRuns(t *testing.T) {
	sk := scriptSkill(t, "read in; echo \"got $in in $(pwd)\"\n", "", 0)
	out, err := runner(nil).Run(context.Background(), sk, []byte("hello"))
	if err != nil || out.Exit != 0 || !strings.HasPrefix(out.Stdout, "got hello in /") {
		t.Fatalf("out %+v, %v", out, err)
	}
}

// 3.7 done when: a variable outside the allowlist is not passed.
func TestEnvOutsideAllowlistNotPassed(t *testing.T) {
	sk := scriptSkill(t, "env\n", "  env: [LANG, ABSENT]\n", 0)
	out, err := runner(map[string]string{"FOO": "leak", "LANG": "uk_UA.UTF-8", "TELEGRAM_BOT_TOKEN": "tok"}).
		Run(context.Background(), sk, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"FOO=", "leak", "TELEGRAM", "tok", "ABSENT"} {
		if strings.Contains(out.Stdout, bad) {
			t.Fatalf("%q in the environment:\n%s", bad, out.Stdout)
		}
	}
	for _, want := range []string{"LANG=uk_UA.UTF-8", "PATH=/usr/local/bin:/usr/bin:/bin", "TZ=Europe/Kyiv", "HOME=/"} {
		if !strings.Contains(out.Stdout, want) {
			t.Fatalf("%q missing:\n%s", want, out.Stdout)
		}
	}
}

// Review focus 5: a script that hangs is killed with its children at the
// timeout.
func TestHangingScriptKilledWithGroup(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	sk := scriptSkill(t, "sleep 30 &\necho $! > "+pidFile+"\nwait\n", "", 300*time.Millisecond)
	start := time.Now()
	_, err := runner(nil).Run(context.Background(), sk, nil)
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
	b, _ := os.ReadFile(pidFile)
	pid := strings.TrimSpace(string(b))
	if pid == "" {
		t.Fatal("the script did not start its child")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := syscall.Kill(atoi(pid), 0); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child %s outlived the script", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestFloodCapped(t *testing.T) {
	sk := scriptSkill(t, "yes flood | head -c 1000000\n", "", 0)
	out, err := runner(nil).Run(context.Background(), sk, nil)
	if err != nil || !out.Truncated || len(out.Stdout) > DefaultRunConfig().MaxOut {
		t.Fatalf("truncated %v, %d bytes, %v", out.Truncated, len(out.Stdout), err)
	}
}

func TestInputCapped(t *testing.T) {
	sk := scriptSkill(t, "cat\n", "", 0)
	if _, err := runner(nil).Run(context.Background(), sk, make([]byte, DefaultRunConfig().MaxIn+1)); err == nil {
		t.Fatal("an input over the cap ran")
	}
}

func TestNonZeroExit(t *testing.T) {
	sk := scriptSkill(t, "echo oops >&2\nexit 3\n", "", 0)
	out, err := runner(nil).Run(context.Background(), sk, nil)
	if err != nil || out.Exit != 3 || !strings.Contains(out.Stderr, "oops") {
		t.Fatalf("out %+v, %v", out, err)
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// A script whose child keeps stdout open after it exits still returns
// what it printed.
func TestForkingScriptKeepsItsOutput(t *testing.T) {
	sk := scriptSkill(t, "echo before\n(sleep 5) &\n", "", 0)
	out, err := runner(nil).Run(context.Background(), sk, nil)
	if err != nil || !strings.Contains(out.Stdout, "before") {
		t.Fatalf("out %+v, %v", out, err)
	}
}

// Review: a file changed after load is checked again before it runs.
func TestScriptRecheckedAtRun(t *testing.T) {
	sk := scriptSkill(t, "echo hi\n", "", 0)
	if err := os.Chmod(sk.Dir+"/run.sh", 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := runner(nil).Run(context.Background(), sk, nil); err == nil {
		t.Fatal("a script made writable by others after load ran")
	}
}
