package update

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDecide(t *testing.T) {
	ok := func(string) error { return nil }
	bad := func(string) error { return errors.New("no") }
	pend := func(n int) *Pending { return &Pending{Version: "v0.3.0", Attempts: n} }
	for _, tc := range []struct {
		name   string
		st     State
		self   bool
		image  string
		verify func(string) error
		want   Decision
	}{
		{"no state", State{}, false, "v0.2.1", bad, Decision{Action: ActionRun}},
		{"the floor", State{Previous: "v0.2.1"}, false, "v0.2.1", bad, Decision{Action: ActionRun}},
		{"self is active", State{Active: "v0.3.0", Pending: pend(5)}, true, "v0.2.1", bad, Decision{Action: ActionRun}},
		{"confirmed active", State{Active: "v0.3.0"}, false, "v0.2.1", ok, Decision{Action: ActionExec}},
		{"first attempt", State{Active: "v0.3.0", Pending: pend(0)}, false, "v0.2.1", ok, Decision{Action: ActionExec}},
		{"second attempt", State{Active: "v0.3.0", Pending: pend(1)}, false, "v0.2.1", ok, Decision{Action: ActionExec}},
		{"two attempts used", State{Active: "v0.3.0", Pending: pend(2)}, false, "v0.2.1", ok, Decision{Action: ActionFallBack, Reason: ReasonFailed}},
		{"three attempts", State{Active: "v0.3.0", Pending: pend(3)}, false, "v0.2.1", bad, Decision{Action: ActionFallBack, Reason: ReasonFailed}},
		{"does not verify", State{Active: "v0.3.0", Pending: pend(0)}, false, "v0.2.1", bad, Decision{Action: ActionFallBack, Reason: ReasonUnverified}},
		{"confirmed does not verify", State{Active: "v0.3.0"}, false, "v0.2.1", bad, Decision{Action: ActionFallBack, Reason: ReasonUnverified}},
		{"newer image", State{Active: "v0.3.0"}, false, "v0.4.0", ok, Decision{Action: ActionImage}},
		{"image of the same version", State{Active: "v0.3.0"}, false, "v0.3.0", ok, Decision{Action: ActionImage}},
		{"image without a release version", State{Active: "v0.3.0"}, false, "dev", ok, Decision{Action: ActionImage}},
		{"image after the release's tag", State{Active: "v0.3.0"}, false, "v0.3.0-2-gabc1234", ok, Decision{Action: ActionImage}},
		{"dirty image of the release's tag", State{Active: "v0.3.0"}, false, "v0.3.0-dirty", ok, Decision{Action: ActionImage}},
		{"image after a newer tag", State{Active: "v0.3.0"}, false, "v0.3.1-1-gabc1234-dirty", ok, Decision{Action: ActionImage}},
		{"image after an older tag", State{Active: "v0.3.0"}, false, "v0.2.1-5-gabc1234", ok, Decision{Action: ActionExec}},
		{"pre-release image", State{Active: "v0.3.0"}, false, "v0.3.0-rc.1", ok, Decision{Action: ActionExec}},
		{"newer image over a failed release", State{Active: "v0.3.0", Pending: pend(2)}, false, "v0.4.0", bad, Decision{Action: ActionImage}},
		{"newer image, self is active", State{Active: "v0.3.0"}, true, "v0.4.0", ok, Decision{Action: ActionRun}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.st, tc.self, tc.image, tc.verify); got != tc.want {
				t.Fatalf("%+v, want %+v", got, tc.want)
			}
		})
	}
}

// launchKit is a store with a live database, a fake exec and a log.
type launchKit struct {
	*kit
	db    string
	execs []string
	log   bytes.Buffer
	self  string
	image string // the floor's version
	exec  func(string) error
}

// fakeMigrations reads a "database" whose content is its migrations, one
// word each.
func fakeMigrations(_ context.Context, path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(b)), nil
}

func newLaunchKit(t *testing.T) *launchKit {
	t.Helper()
	k := &launchKit{self: "/floor/ganglion", image: "v0.2.1"}
	k.kit = newKit(t, WithCopyDB(func(_ context.Context, dst string) error {
		return os.WriteFile(dst, []byte("a"), 0o600) // before.db: one migration
	}))
	k.db = filepath.Join(t.TempDir(), "ganglion.db")
	k.write(k.db, "a b") // the failed version added b
	k.exec = func(string) error { return nil }
	return k
}

func (k *launchKit) write(path, content string) {
	k.t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		k.t.Fatal(err)
	}
}

func (k *launchKit) launcher() Launcher {
	return Launcher{Store: k.store, DB: k.db, Self: k.self, Image: k.image, Migrations: fakeMigrations, Log: &k.log,
		Exec: func(p string) error { k.execs = append(k.execs, p); return k.exec(p) }}
}

func (k *launchKit) state() State {
	k.t.Helper()
	st, err := k.store.State()
	if err != nil {
		k.t.Fatal(err)
	}
	return st
}

func (k *launchKit) launch() error { return k.launcher().Launch(context.Background()) }

func TestLaunchWithNoStateRunsOn(t *testing.T) {
	k := newLaunchKit(t)
	if err := k.launch(); err != nil || len(k.execs) != 0 {
		t.Fatalf("%v %v", err, k.execs)
	}
	if k.stateBytes() != nil {
		t.Fatal("a state was written")
	}
}

func TestLaunchExecsTheActiveReleaseAndCountsAttempts(t *testing.T) {
	k := newLaunchKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 2; want++ {
		if err := k.launch(); err != nil {
			t.Fatal(err)
		}
		if got := k.state().Pending.Attempts; got != want {
			t.Fatalf("attempts %d, want %d", got, want)
		}
	}
	if len(k.execs) != 2 || k.execs[0] != k.store.BinaryPath("v0.3.0") {
		t.Fatalf("%v", k.execs)
	}
}

func TestLaunchOfANewerImageDropsTheReleases(t *testing.T) {
	k := newLaunchKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	confirmed := k.state().Confirmed
	k.image = "v0.4.0"
	if err := k.launch(); err != nil || len(k.execs) != 0 {
		t.Fatalf("%v %v", err, k.execs)
	}
	if st := k.state(); st.Active != "" || st.Previous != "" || st.Pending != nil || st.Fallback != nil || st.Confirmed != confirmed {
		t.Fatalf("%+v", st)
	}
	if _, err := os.Stat(filepath.Dir(k.store.BinaryPath("v0.3.0"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the release folder stays: %v", err)
	}
	if log := k.log.String(); !strings.Contains(log, "v0.4.0") || !strings.Contains(log, "v0.3.0") {
		t.Fatalf("log %q", log)
	}
	if err := k.launch(); err != nil || len(k.execs) != 0 {
		t.Fatalf("the next start: %v %v", err, k.execs)
	}
}

func TestLaunchOfAnOlderImageSaysItRunsTheRelease(t *testing.T) {
	k := newLaunchKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	if err := k.launch(); err != nil || len(k.execs) != 1 {
		t.Fatalf("%v %v", err, k.execs)
	}
	if log := k.log.String(); !strings.Contains(log, "v0.2.1") || !strings.Contains(log, "older") {
		t.Fatalf("log %q", log)
	}
}

func TestLaunchThirdStartFallsBackAndRestoresTheDatabase(t *testing.T) {
	k := newLaunchKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := k.launch(); err != nil {
			t.Fatal(err)
		}
	}
	if len(k.execs) != 2 {
		t.Fatalf("execs %v", k.execs)
	}
	st := k.state()
	fb := st.Fallback
	if st.Active != "" || st.Pending != nil || fb == nil || fb.From != "v0.3.0" || fb.To != "" || !fb.RestoredDB ||
		fb.Reason != ReasonFailed || !fb.At.Equal(testNow) {
		t.Fatalf("%+v %+v", st, fb)
	}
	if b, _ := os.ReadFile(k.db); string(b) != "a" {
		t.Fatalf("database %q", b)
	}
	if _, err := os.Stat(filepath.Join(k.store.Dir(), "v0.3.0")); err == nil {
		t.Fatal("the failed release is still there")
	}
}

func TestLaunchKeepsTheDatabaseWhenNothingWasMigrated(t *testing.T) {
	k := newLaunchKit(t)
	k.write(k.db, "a")
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := k.launch(); err != nil {
			t.Fatal(err)
		}
	}
	if fb := k.state().Fallback; fb == nil || fb.RestoredDB {
		t.Fatalf("%+v", fb)
	}
	if b, _ := os.ReadFile(k.db); string(b) != "a" {
		t.Fatalf("database %q", b)
	}
}

func TestRestoreRemovesTheWriteAheadFiles(t *testing.T) {
	k := newLaunchKit(t)
	k.write(k.db+"-wal", "w")
	k.write(k.db+"-shm", "s")
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := k.launch(); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(k.db + s); err == nil {
			t.Fatalf("%s is left", s)
		}
	}
}

func TestLaunchOfTheActiveReleaseItselfRunsOn(t *testing.T) {
	k := newLaunchKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	self, err := filepath.EvalSymlinks(k.store.BinaryPath("v0.3.0"))
	if err != nil {
		t.Fatal(err)
	}
	k.self = self
	before := k.stateBytes()
	if err := k.launch(); err != nil || len(k.execs) != 0 {
		t.Fatalf("%v %v", err, k.execs)
	}
	if !bytes.Equal(before, k.stateBytes()) {
		t.Fatal("state changed")
	}
}

func TestLaunchFallsBackToThePreviousRelease(t *testing.T) {
	k := newLaunchKit(t)
	for _, v := range []string{"v0.3.0", "v0.4.0"} {
		if _, err := k.install(v, "v0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		if err := k.launch(); err != nil {
			t.Fatal(err)
		}
	}
	// v0.4.0 twice, then v0.3.0 once after the fall back.
	want := []string{k.store.BinaryPath("v0.4.0"), k.store.BinaryPath("v0.4.0"), k.store.BinaryPath("v0.3.0")}
	if strings.Join(k.execs, ",") != strings.Join(want, ",") {
		t.Fatalf("%v", k.execs)
	}
	st := k.state()
	if st.Active != "v0.3.0" || st.Previous != "" || st.Pending != nil || st.Fallback.From != "v0.4.0" || st.Fallback.To != "v0.3.0" {
		t.Fatalf("%+v %+v", st, st.Fallback)
	}
}

// Abuse: a release that was never installed is never executed.
func TestLaunchNeverExecsAPlantedBinary(t *testing.T) {
	k := newLaunchKit(t)
	dir := filepath.Join(k.store.Dir(), "v9.9.9")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	k.write(filepath.Join(dir, BinaryFile), "evil")
	k.write(filepath.Join(k.store.Dir(), StateFile), `{"active":"v9.9.9","previous":"","confirmed":""}`)
	if err := k.launch(); err != nil {
		t.Fatal(err)
	}
	if len(k.execs) != 0 {
		t.Fatalf("executed %v", k.execs)
	}
	if st := k.state(); st.Active != "" || st.Fallback == nil || st.Fallback.Reason != ReasonUnverified || st.Fallback.From != "v9.9.9" {
		t.Fatalf("%+v %+v", st, st.Fallback)
	}
}

// Abuse: a binary replaced after the install is not run.
func TestLaunchNeverExecsAReplacedBinary(t *testing.T) {
	k := newLaunchKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	bin := k.store.BinaryPath("v0.3.0")
	if err := os.Chmod(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	k.write(bin, "replaced")
	if err := k.launch(); err != nil {
		t.Fatal(err)
	}
	if len(k.execs) != 0 {
		t.Fatalf("executed %v", k.execs)
	}
	if fb := k.state().Fallback; fb == nil || fb.Reason != ReasonUnverified {
		t.Fatalf("%+v", fb)
	}
}

// Abuse: a version that is a path is refused, not followed.
func TestLaunchRefusesAStateNamingAPath(t *testing.T) {
	k := newLaunchKit(t)
	k.write(filepath.Join(k.store.Dir(), StateFile), `{"active":"../x","previous":"","confirmed":""}`)
	if err := k.launch(); !errors.Is(err, ErrBadState) || len(k.execs) != 0 {
		t.Fatalf("%v %v", err, k.execs)
	}
}

func TestLaunchWhileAnInstallRunsStartsAsItIs(t *testing.T) {
	k := newLaunchKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockDir(filepath.Join(k.store.Dir(), LockFile))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	before := k.stateBytes()
	if err := k.launch(); err != nil || len(k.execs) != 0 {
		t.Fatalf("%v %v", err, k.execs)
	}
	if !bytes.Equal(before, k.stateBytes()) || !strings.Contains(k.log.String(), "install is running") {
		t.Fatalf("log %q", k.log.String())
	}
}

func TestLaunchOnANoexecVolumeFallsBack(t *testing.T) {
	k := newLaunchKit(t)
	k.exec = func(string) error { return &os.PathError{Op: "exec", Path: "x", Err: syscall.EACCES} }
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	if err := k.launch(); err != nil {
		t.Fatal(err)
	}
	st := k.state()
	if len(k.execs) != 1 || st.Active != "" || st.Fallback == nil || st.Fallback.Reason != ReasonNoExec {
		t.Fatalf("%v %+v %+v", k.execs, st, st.Fallback)
	}
}

func TestCleanStopTakesAnAttemptBack(t *testing.T) {
	k := newLaunchKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	if err := k.launch(); err != nil {
		t.Fatal(err)
	}
	if err := k.store.CleanStop("v0.2.1"); err != nil || k.state().Pending.Attempts != 1 {
		t.Fatalf("another version took an attempt back: %v", err)
	}
	for range 2 { // the second call must not go below zero
		if err := k.store.CleanStop("v0.3.0"); err != nil {
			t.Fatal(err)
		}
	}
	if got := k.state().Pending.Attempts; got != 0 {
		t.Fatalf("attempts %d", got)
	}
}

func TestCleanStopWithNoPendingChangesNothing(t *testing.T) {
	k := newLaunchKit(t)
	if err := k.store.CleanStop("v0.3.0"); err != nil || k.stateBytes() != nil {
		t.Fatalf("%v", err)
	}
}
