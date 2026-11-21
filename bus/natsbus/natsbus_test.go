package natsbus

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/texts"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"
)

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

func openEmbedded(t *testing.T, dir string) *Bus {
	t.Helper()
	b, err := Open(context.Background(), DefaultConfig(dir), quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	return b
}

// An embedded bus makes its bucket and streams with the settings of R-BUS-2,
// and Ensure can run again over them.
func TestEmbeddedEnsureCreatesTheBucketAndStreams(t *testing.T) {
	ctx := context.Background()
	b := openEmbedded(t, filepath.Join(t.TempDir(), "bus"))
	for range 2 {
		if err := b.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
	}
	js := b.JetStream()
	kv, err := js.KeyValue(ctx, bus.BucketCells)
	if err != nil {
		t.Fatal(err)
	}
	st, err := kv.Status(ctx)
	if err != nil || st.History() != 64 {
		t.Fatalf("cells history = %v, %v; want 64", st.History(), err)
	}
	for name, subject := range map[string]string{bus.StreamNotify: "g.notify.>", bus.StreamEvents: "g.ev.>"} {
		s, err := js.Stream(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		cfg := s.CachedInfo().Config
		if cfg.Storage != jetstream.FileStorage || cfg.MaxAge != 7*24*time.Hour || cfg.Duplicates != 2*time.Minute ||
			len(cfg.Subjects) != 1 || cfg.Subjects[0] != subject {
			t.Errorf("%s = %+v", name, cfg)
		}
	}
	publishOne(t, b)
	h := b.Health(ctx)
	if h.Mode != bus.ModeEmbedded || !h.Connected || h.StoreBytes == 0 {
		t.Errorf("health = %+v, want embedded, connected and a store in use", h)
	}
}

// A duplicate Nats-Msg-Id within the window is dropped by the stream.
func TestStreamsDropDuplicates(t *testing.T) {
	ctx := context.Background()
	b := openEmbedded(t, t.TempDir())
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	subject, err := bus.Notify("reader")
	if err != nil {
		t.Fatal(err)
	}
	var dups []bool
	for range 2 {
		ack, err := b.JetStream().Publish(ctx, subject, []byte("x"), jetstream.WithMsgID("m1"))
		if err != nil {
			t.Fatal(err)
		}
		dups = append(dups, ack.Duplicate)
	}
	if dups[0] || !dups[1] {
		t.Fatalf("duplicate flags = %v, want [false true]", dups)
	}
}

// What was written survives a restart on the same folder, and a bus with no
// folder leaves nothing behind.
func TestStorePersistsAndTemporaryStoreIsRemoved(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	b := openEmbedded(t, dir)
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	kv, _ := b.JetStream().KeyValue(ctx, bus.BucketCells)
	if _, err := kv.PutString(ctx, "m.c", "v"); err != nil {
		t.Fatal(err)
	}
	if err := b.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	b = openEmbedded(t, dir)
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	kv, _ = b.JetStream().KeyValue(ctx, bus.BucketCells)
	if e, err := kv.Get(ctx, "m.c"); err != nil || string(e.Value()) != "v" {
		t.Fatalf("after restart: %v, %v", e, err)
	}

	tmp, err := Open(ctx, DefaultConfig(""), quiet())
	if err != nil {
		t.Fatal(err)
	}
	folder := tmp.tmpDir
	if err := tmp.Shutdown(ctx); err != nil || folder == "" {
		t.Fatalf("shutdown = %v, folder %q", err, folder)
	}
	if exists(folder) {
		t.Errorf("the temporary store %s was not removed", folder)
	}
}

// Shutdown leaves no goroutine of the server or the client behind.
func TestShutdownLeavesNoGoroutine(t *testing.T) {
	ctx := context.Background()
	before := runtime.NumGoroutine()
	b, err := Open(ctx, DefaultConfig(t.TempDir()), quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Shutdown(ctx); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines %d -> %d\n%s", before, after, buf[:runtime.Stack(buf, true)])
	}
}

// The embedded server's warnings and errors go to the node's log.
func TestServerLogGoesToSlog(t *testing.T) {
	var out bytes.Buffer
	l := slogLogger{log: slog.New(slog.NewTextHandler(&out, nil))}
	l.Noticef("hidden")
	l.Warnf("slow %s", "consumer")
	l.Errorf("broken %d", 1)
	got := out.String()
	if strings.Contains(got, "hidden") || !strings.Contains(got, "slow consumer") || !strings.Contains(got, "broken 1") {
		t.Fatalf("log = %q", got)
	}
}

// startExternal runs a second server with a TCP port, as an operator's would
// be, and returns its address.
func startExternal(t *testing.T, opts *server.Options) string {
	t.Helper()
	url, _ := startExternalServer(t, opts)
	return url
}

func startExternalServer(t *testing.T, opts *server.Options) (string, *server.Server) {
	t.Helper()
	opts.Host, opts.Port, opts.JetStream, opts.StoreDir, opts.NoSigs = "127.0.0.1", -1, true, t.TempDir(), true
	srv, err := server.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("the external server is not ready")
	}
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })
	return "nats://" + srv.Addr().String(), srv
}

func openExternal(t *testing.T, url, creds string) (*Bus, error) {
	t.Helper()
	cfg := DefaultConfig("")
	cfg.Mode, cfg.URLs, cfg.Credentials, cfg.StartTimeout = bus.ModeExternal, []string{url}, creds, 3*time.Second
	b, err := Open(context.Background(), cfg, quiet())
	if err == nil {
		t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
	}
	return b, err
}

func TestExternalServer(t *testing.T) {
	rec := telemetrytest.Install(t)
	ctx := context.Background()
	b, err := openExternal(t, startExternal(t, &server.Options{}), "")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := b.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.JetStream().Stream(ctx, bus.StreamEvents); err != nil {
		t.Fatal(err)
	}
	publishOne(t, b)
	if h := b.Health(ctx); h.Mode != bus.ModeExternal || !h.Connected || h.StoreBytes == 0 {
		t.Errorf("health = %+v", h)
	}
	for name, want := range map[string]float64{"ganglion.bus.connected": 1} {
		if got := rec.Value(t, name, map[string]string{"mode": "external"}); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if got := rec.Value(t, "ganglion.bus.store_bytes", map[string]string{"mode": "external"}); got <= 0 {
		t.Errorf("store_bytes = %v, want some", got)
	}
}

// credsServer is an operator-mode server with one account that has
// JetStream, and the .creds text of one of its users.
func credsServer(t *testing.T) (url, creds string) {
	t.Helper()
	op, acc, user := must(nkeys.CreateOperator()), must(nkeys.CreateAccount()), must(nkeys.CreateUser())
	opPub, accPub, userPub := must(op.PublicKey()), must(acc.PublicKey()), must(user.PublicKey())
	ac := jwt.NewAccountClaims(accPub)
	ac.Limits.JetStreamLimits = jwt.JetStreamLimits{MemoryStorage: -1, DiskStorage: -1, Streams: -1, Consumer: -1}
	accJWT := must(ac.Encode(op))
	sys := must(nkeys.CreateAccount())
	sysPub := must(sys.PublicKey())
	userJWT := must(jwt.NewUserClaims(userPub).Encode(acc))
	seed := must(user.Seed())
	res := &server.MemAccResolver{}
	if err := res.Store(accPub, accJWT); err != nil {
		t.Fatal(err)
	}
	if err := res.Store(sysPub, must(jwt.NewAccountClaims(sysPub).Encode(op))); err != nil {
		t.Fatal(err)
	}
	url = startExternal(t, &server.Options{TrustedKeys: []string{opPub}, AccountResolver: res, SystemAccount: sysPub})
	return url, string(must(jwt.FormatUserConfig(userJWT, seed)))
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// A server that needs credentials takes the ones from memory and refuses a
// connection without them, with an error that names the cause and not the
// credentials.
func TestExternalServerWithCredentials(t *testing.T) {
	url, creds := credsServer(t)
	b, err := openExternal(t, url, creds)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := openExternal(t, url, ""); err == nil || !strings.Contains(err.Error(), "natsbus: connect") {
		t.Fatalf("without credentials: %v", err)
	}
}

func TestOpenRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  func(*Config)
		want error
	}{
		"bad scheme":        {func(c *Config) { c.Mode, c.URLs = bus.ModeExternal, []string{"http://x:4222"} }, bus.ErrBadURL},
		"password in url":   {func(c *Config) { c.Mode, c.URLs = bus.ModeExternal, []string{"nats://u:p@x:4222"} }, bus.ErrBadURL},
		"nothing to join":   {func(c *Config) { c.Mode = bus.ModeExternal }, nil},
		"urls for embedded": {func(c *Config) { c.URLs = []string{"nats://x:4222"} }, nil},
		"unknown mode":      {func(c *Config) { c.Mode = "cloud" }, nil},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig(t.TempDir())
			tc.cfg(&cfg)
			_, err := Open(context.Background(), cfg, quiet())
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

// A server that refuses the connection is an error that says so.
func TestOpenRefusedConnection(t *testing.T) {
	_, err := openExternal(t, "nats://127.0.0.1:1", "")
	if err == nil || !strings.Contains(err.Error(), "natsbus: connect") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnsureBeforeConnectIsRefused(t *testing.T) {
	if err := (&Bus{}).Ensure(context.Background()); !errors.Is(err, bus.ErrNotConnected) {
		t.Fatalf("error = %v", err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// publishOne puts a message in the NOTIFY stream, so the store holds bytes.
func publishOne(t *testing.T, b *Bus) {
	t.Helper()
	if _, err := b.JetStream().Publish(context.Background(), "g.notify.reader", []byte("hello")); err != nil {
		t.Fatal(err)
	}
}

// /status shows the bus while it is connected, fails when the server goes
// away, and the loss is counted.
func TestStatusAndDisconnectCounter(t *testing.T) {
	rec := telemetrytest.Install(t)
	ctx := context.Background()
	url, srv := startExternalServer(t, &server.Options{})
	b, err := openExternal(t, url, "")
	if err != nil {
		t.Fatal(err)
	}
	checks, err := b.Status(ctx, status.Scope{})
	if err != nil || len(checks) != 1 || checks[0].State != status.OK || checks[0].Line != texts.StatusBusOK || checks[0].Vars["mode"] != "external" {
		t.Fatalf("status = %+v, %v", checks, err)
	}
	srv.Shutdown()
	srv.WaitForShutdown()
	deadline := time.Now().Add(5 * time.Second)
	for b.nc.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	checks, _ = b.Status(ctx, status.Scope{})
	if len(checks) != 1 || checks[0].State != status.Fail || checks[0].Line != texts.StatusBusDown {
		t.Fatalf("status after the loss = %+v", checks)
	}
	if got := rec.Value(t, "ganglion.bus.disconnects", nil); got != 1 {
		t.Errorf("disconnects = %v, want 1", got)
	}
	if got := rec.Value(t, "ganglion.bus.connected", map[string]string{"mode": "external"}); got != 0 {
		t.Errorf("connected = %v, want 0", got)
	}
}
