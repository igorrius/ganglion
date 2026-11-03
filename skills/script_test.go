//go:build linux || darwin

package skills

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
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

func runner(lookup map[string]string) *Runner {
	cfg := DefaultRunConfig()
	cfg.TZ = "Europe/Kyiv"
	cfg.Env = func(k string) (string, bool) { v, ok := lookup[k]; return v, ok }
	return NewRunner(cfg)
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
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat("/proc/" + pid); os.IsNotExist(err) || pid == "" {
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
