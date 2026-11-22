package natsbus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/nats-io/nats.go/micro"
)

// serveX serves port x of module a with h.
func serveX(t *testing.T, b *Bus, h bus.EndpointHandler) (stop func() error) {
	t.Helper()
	subject, err := bus.Call("core", "a", "x")
	if err != nil {
		t.Fatal(err)
	}
	stop, err = b.Serve("a", "3.0.0", []bus.Endpoint{{Name: "x", Subject: subject, Metadata: map[string]string{"in": "number", "out": "number"}, Handler: h}})
	if err != nil {
		t.Fatal(err)
	}
	return stop
}

func within(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// A request reaches the endpoint with its headers and body, and the answer
// comes back.
func TestACallIsAnswered(t *testing.T) {
	b := openEmbedded(t, t.TempDir())
	var got bus.Request
	stop := serveX(t, b, func(_ context.Context, r bus.Request) ([]byte, error) {
		got = r
		return []byte(`"ok"`), nil
	})
	defer func() { _ = stop() }()
	headers := map[string]string{bus.HeaderDepth: "2", bus.HeaderDeadline: "1234"}
	ans, err := b.Request(within(t, 5*time.Second), "g.rpc.core.a.x", headers, []byte(`7`))
	if err != nil || string(ans) != `"ok"` {
		t.Fatalf("answer %q, err %v", ans, err)
	}
	if string(got.Payload) != "7" || got.Headers[bus.HeaderDepth] != "2" || got.Headers[bus.HeaderDeadline] != "1234" {
		t.Fatalf("the endpoint saw %+v", got)
	}
}

// The service is discoverable, named after the module at its version, with
// the shapes in the endpoint's metadata.
func TestAServiceIsRegisteredWithMicro(t *testing.T) {
	b := openEmbedded(t, t.TempDir())
	stop := serveX(t, b, func(context.Context, bus.Request) ([]byte, error) { return []byte(`1`), nil })
	defer func() { _ = stop() }()
	subject, err := micro.ControlSubject(micro.InfoVerb, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := b.nc.RequestWithContext(within(t, 5*time.Second), subject, nil)
	if err != nil {
		t.Fatal(err)
	}
	var info micro.Info
	if err := json.Unmarshal(msg.Data, &info); err != nil {
		t.Fatal(err)
	}
	if info.Name != "a" || info.Version != "3.0.0" || len(info.Endpoints) != 1 {
		t.Fatalf("info %+v", info)
	}
	e := info.Endpoints[0]
	if e.Name != "x" || e.Subject != "g.rpc.core.a.x" || e.Metadata["in"] != "number" || e.Metadata["out"] != "number" {
		t.Fatalf("endpoint %+v", e)
	}
}

// With nobody serving the subject the request fails at once, not at the
// deadline; so does one whose service was stopped.
func TestNoResponderIsAnErrorAtOnce(t *testing.T) {
	b := openEmbedded(t, t.TempDir())
	started := time.Now()
	_, err := b.Request(within(t, 10*time.Second), "g.rpc.core.a.x", nil, []byte(`1`))
	if !errors.Is(err, bus.ErrNoResponder) {
		t.Fatalf("error %v, want no responder", err)
	}
	stop := serveX(t, b, func(context.Context, bus.Request) ([]byte, error) { return []byte(`1`), nil })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Request(within(t, 10*time.Second), "g.rpc.core.a.x", nil, []byte(`1`)); !errors.Is(err, bus.ErrNoResponder) {
		t.Fatalf("after stop: error %v, want no responder", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("no responder took the time of a deadline")
	}
}

// An answer that comes after the caller's deadline is a timeout of the
// caller's own context.
func TestACallTimesOut(t *testing.T) {
	b := openEmbedded(t, t.TempDir())
	release := make(chan struct{})
	stop := serveX(t, b, func(context.Context, bus.Request) ([]byte, error) {
		<-release
		return []byte(`1`), nil
	})
	defer func() { _ = stop() }()
	defer close(release)
	_, err := b.Request(within(t, 100*time.Millisecond), "g.rpc.core.a.x", nil, []byte(`1`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v, want the deadline", err)
	}
}

// An error answer carries its code and description; an error that is not a
// *bus.CallError is a failure with no text of its own.
func TestAnErrorAnswerCarriesItsCode(t *testing.T) {
	b := openEmbedded(t, t.TempDir())
	var next error
	stop := serveX(t, b, func(context.Context, bus.Request) ([]byte, error) { return nil, next })
	defer func() { _ = stop() }()
	next = &bus.CallError{Code: bus.CodeDeep, Description: "the chain is too deep"}
	_, err := b.Request(within(t, 5*time.Second), "g.rpc.core.a.x", nil, []byte(`1`))
	var ce *bus.CallError
	if !errors.As(err, &ce) || ce.Code != bus.CodeDeep || ce.Description != "the chain is too deep" {
		t.Fatalf("error %v, want the deep answer", err)
	}
	next = errors.New("secret detail of the module")
	_, err = b.Request(within(t, 5*time.Second), "g.rpc.core.a.x", nil, []byte(`1`))
	if !errors.As(err, &ce) || ce.Code != bus.CodeFailed || ce.Description != "the call failed" {
		t.Fatalf("error %v, want a failure with no text", err)
	}
}
