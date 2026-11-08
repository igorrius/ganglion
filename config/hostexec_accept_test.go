//go:build linux

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/journal"
	"github.com/samber/do/v2"
)

// commandNode is the agent node of the end-to-end checks: the real container
// with the real sandbox, its runner running, and the secrets it was given.
type commandNode struct {
	reg           *tool.Registry
	pol           *policy.Policy
	db, tokenFile string
	secrets       []string
	n             int // calls made, for each call's own key
}

// startCommandNode builds the node as `ganglion run` does, with a bot token
// in a TELEGRAM_BOT_TOKEN_FILE and a provider key in the environment, and
// runs the runner of host commands. Each of with changes the bootstrap
// before the node is built. It skips where the sandbox cannot be ready.
func startCommandNode(t *testing.T, with ...func(*Bootstrap)) *commandNode {
	t.Helper()
	notDumpable(t)
	if !hostSandboxReady() {
		t.Skip("the command sandbox is not ready on this machine")
	}
	const token, key = "7001:accept-token-secret-5511", "sk-accept-provider-secret-5512"
	tokenFile := filepath.Join(t.TempDir(), "bot-file")
	if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", tokenFile)
	t.Setenv("OR_KEY", key)
	b := agentConfig(t)
	b.BotToken, b.Agent.Keys = token, map[string]string{"or": key}
	for _, w := range with {
		w(&b)
	}

	i := New(context.Background(), b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	runner := do.MustInvoke[*host.Runner](i)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	n := &commandNode{reg: do.MustInvoke[*tool.Registry](i), pol: do.MustInvoke[*policy.Policy](i), db: b.DB,
		tokenFile: tokenFile, secrets: []string{token, key}}
	if !n.reg.Offered(builtinHostExec, true) {
		t.Fatal("host.exec is not offered with the sandbox ready")
	}
	return n
}

// builtinHostExec is host.exec's name.
const builtinHostExec = "host.exec"

// call is a host.exec call of argv by the admin in their chat.
func (n *commandNode) call(argv ...string) tool.Call {
	n.n++
	args, err := json.Marshal(map[string]any{"argv": argv, "reason": "acceptance"})
	if err != nil {
		panic(err)
	}
	return tool.Call{TurnID: "01ACCEPT", N: n.n, Conv: "telegram:42", Transport: "telegram", Principal: "telegram:user:42",
		Admin: true, Name: builtinHostExec, Args: args}
}

// runApproved runs argv as a pressed call, which runs whatever its class but
// forbidden, and returns what the model would read.
func (n *commandNode) runApproved(t *testing.T, argv ...string) string {
	t.Helper()
	c := n.call(argv...)
	c.Approved = true
	res, err := n.reg.Run(context.Background(), c)
	if err != nil {
		t.Fatalf("%v: %v", argv, err)
	}
	if !res.External {
		t.Errorf("%v: command output is outside content", argv)
	}
	for _, s := range n.secrets {
		if strings.Contains(res.Text, s) {
			t.Errorf("%v printed a secret of the node:\n%s", argv, res.Text)
		}
	}
	return res.Text
}

// failed reports whether a command's text says it exited non-zero.
func failed(text string) bool {
	return strings.HasPrefix(text, "exit ") && !strings.HasPrefix(text, "exit 0 ")
}

// design §10 checks 4 and 5, abuse test (conventions, section 11): a pressed
// command runs in the node's sandbox, so it cannot read the node's
// environment, database or secret files, its environment holds only the
// allowlist, and sudo gains no root under no_new_privs.
func TestAPressedCommandCannotReachTheNodesSecrets(t *testing.T) {
	n := startCommandNode(t)
	for _, argv := range [][]string{
		{"cat", fmt.Sprintf("/proc/%d/environ", os.Getpid())},
		{"cat", n.db},
		{"cat", n.tokenFile},
		{"bash", "-c", "cat /proc/$PPID/environ"},
	} {
		if out := n.runApproved(t, argv...); !failed(out) || !strings.Contains(out, "Permission denied") {
			t.Errorf("%v was not refused:\n%s", argv, out)
		}
	}
	if out := n.runApproved(t, "bash", "-c", "sudo -n true"); !failed(out) {
		t.Errorf("sudo -n true succeeded in the sandbox:\n%s", out)
	}
}

// design §10 check 5: a pressed script reads its environment, which holds
// only the allowlist, never the node's keys or the paths of its secrets.
func TestACommandSeesOnlyTheAllowedEnvironment(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	n := startCommandNode(t)
	out := n.runApproved(t, "python3", "-c", "import os\nfor k in sorted(os.environ): print('VAR', k)")
	if failed(out) {
		t.Fatalf("python3 failed:\n%s", out)
	}
	// LC_CTYPE is Python's own, set by its C-locale coercion (PEP 538), not
	// passed in.
	allowed := []string{"PATH", "HOME", "LANG", "TZ", "TERM", "LC_CTYPE"}
	var seen []string
	for line := range strings.Lines(out) {
		if k, ok := strings.CutPrefix(strings.TrimSpace(line), "VAR "); ok {
			seen = append(seen, k)
		}
	}
	if !slices.Contains(seen, "PATH") {
		t.Fatalf("the script printed no environment:\n%s", out)
	}
	for _, k := range seen {
		if !slices.Contains(allowed, k) {
			t.Errorf("the command's environment holds %s, outside the allowlist %v", k, allowed)
		}
	}
}

// env prints every variable: the policy forbids it before any press, and
// the tool refuses it even pressed.
func TestEnvIsForbiddenEvenPressed(t *testing.T) {
	n := startCommandNode(t)
	c := n.call("env")
	spec, ok := n.reg.Spec(builtinHostExec)
	if !ok {
		t.Fatal("no host.exec spec")
	}
	if _, d := policy.Decide(8, spec, c); d != journal.DecisionForbidden {
		t.Fatalf("the policy decided %q for env, want forbidden", d)
	}
	if _, err := n.pol.Middleware()(n.reg.Run)(context.Background(), c); !errors.Is(err, tool.ErrForbidden) {
		t.Fatalf("env through the policy: %v", err)
	}
	c.Approved = true
	if res, err := n.reg.Run(context.Background(), c); !errors.Is(err, tool.ErrForbidden) {
		t.Fatalf("env pressed: %+v, %v", res, err)
	}
}

// R-CMD-6 under the real sandbox: a content command on a file git ignores
// asks with the warning, git being asked inside the sandbox; the same
// command on an untracked file it does not ignore asks without it.
func TestTheQuestionWarnsOfAFileGitIgnores(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	n := startCommandNode(t)
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for name, body := range map[string]string{".gitignore": "local.conf\n", "local.conf": "password=x\n", "untracked.conf": "x\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h, ok := n.reg.Lookup(builtinHostExec)
	if !ok {
		t.Fatal("no host.exec")
	}
	q, ok := h.(tool.Questioner)
	if !ok {
		t.Fatal("host.exec asks no question")
	}
	warning, _, _ := strings.Cut(texts.EnglishText(texts.CmdGitIgnored), "{repo}")
	for file, want := range map[string]bool{"local.conf": true, "untracked.conf": false} {
		c := n.call("cat", filepath.Join(repo, file))
		c.Speak = texts.Speaker{}
		text, err := q.Question(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(text, warning); got != want {
			t.Errorf("cat %s: warning %t, want %t:\n%s", file, got, want, text)
		}
	}
}

// ADR-0057, abuse test (conventions, section 11): through host.exec with the
// real sandbox, home and "/", which the sandbox splits around the owner's
// secret stores, may be listed, and so may the node's data folder, yet a
// denied file in it still cannot be read.
func TestACommandListsHomeButReadsNoSecret(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "/" {
		t.Skip("no home folder to list")
	}
	n := startCommandNode(t)
	for _, dir := range []string{home, "/"} {
		if out := n.runApproved(t, "ls", "-a", dir); failed(out) {
			t.Fatalf("ls %s failed:\n%s", dir, out)
		}
	}
	if out := n.runApproved(t, "ls", filepath.Dir(n.db)); failed(out) || !strings.Contains(out, filepath.Base(n.db)) {
		t.Errorf("the data folder was not listed:\n%s", out)
	}
	for _, f := range []string{n.db, n.tokenFile} {
		if out := n.runApproved(t, "cat", f); !failed(out) || !strings.Contains(out, "Permission denied") {
			t.Errorf("cat %s was not refused:\n%s", f, out)
		}
	}
}

// GANGLION_SANDBOX_HIDE_NAMES=on reaches the sandbox: home is not listed.
func TestHiddenNamesKeepHomeUnlisted(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "/" {
		t.Skip("no home folder to list")
	}
	n := startCommandNode(t, func(b *Bootstrap) { b.SandboxHideNames = true })
	if out := n.runApproved(t, "ls", home); !failed(out) || !strings.Contains(out, "Permission denied") {
		t.Fatalf("ls %s with hidden names:\n%s", home, out)
	}
}
