package settings_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/schemas"
	"github.com/igorrius/ganglion/settings"
)

func TestBusDefaultsToTheEmbeddedServer(t *testing.T) {
	for _, src := range []string{"", "bus:\n", "bus:\n  server: embedded\n"} {
		f, err := settings.Parse([]byte(src))
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if f.Bus.Mode() != bus.ModeEmbedded || len(f.Bus.URLs) != 0 || f.Bus.Credentials != "" {
			t.Errorf("%q: bus = %+v", src, f.Bus)
		}
	}
}

func TestExternalBusLoads(t *testing.T) {
	f, err := settings.Parse([]byte("bus:\n  server: external\n  urls: [nats://a.example.com:4222, tls://b.example.com:4222]\n  credentials: store:BUS_CREDS\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Bus.Mode() != bus.ModeExternal || len(f.Bus.URLs) != 2 || f.Bus.Credentials != "store:BUS_CREDS" {
		t.Fatalf("bus = %+v", f.Bus)
	}
	if !slices.Contains(f.StoreNames(), "BUS_CREDS") {
		t.Errorf("StoreNames %v lacks the bus credentials", f.StoreNames())
	}
	text, err := f.Bus.CredentialsText(settings.Sources{Env: fakeSource{}, Store: fakeSource{"BUS_CREDS": "creds text"}})
	if err != nil || text != "creds text" {
		t.Errorf("credentials text = %q, %v", text, err)
	}
	if _, err := f.Bus.CredentialsText(settings.Sources{Env: fakeSource{}, Store: fakeSource{}}); err == nil || !strings.Contains(err.Error(), "ganglion secret set BUS_CREDS") {
		t.Errorf("unset credentials: %v", err)
	}
}

func TestExternalBusOnTheChainLoads(t *testing.T) {
	f, err := settings.Parse([]byte("bus:\n  server: external\n  urls: [nats://a.example.com:4222]\n  trust: chain\n"))
	if err != nil || f.Bus.Trust != settings.TrustChain {
		t.Fatalf("bus = %+v, %v", f.Bus, err)
	}
}

func TestBusIsChecked(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"unknown server":       {"bus: {server: cloud}", "bus.server"},
		"external needs urls":  {"bus: {server: external}", "bus.urls"},
		"urls for embedded":    {"bus: {urls: ['nats://x.example.com:4222']}", "bus.urls"},
		"credentials embedded": {"bus: {credentials: env:BUS_CREDS}", "bus.credentials"},
		"http url":             {"bus: {server: external, urls: ['http://x.example.com']}", "bus.urls[0]"},
		"password in url":      {"bus: {server: external, urls: ['nats://u:hunter2@x.example.com:4222']}", "bus.urls[0]"},
		"literal credentials":  {"bus: {server: external, urls: ['nats://x.example.com'], credentials: hunter2}", "bus.credentials"},
		"unknown key":          {"bus: {server: embedded, port: 4222}", "port"},
		"trust for embedded":   {"bus: {trust: chain}", "bus.trust"},
		"unknown trust":        {"bus: {server: external, urls: ['nats://x.example.com'], trust: hunter2}", "bus.trust"},
		"trust and creds":      {"bus: {server: external, urls: ['nats://x.example.com'], trust: chain, credentials: env:BUS_CREDS}", "bus.trust"},
	} {
		_, err := settings.Parse([]byte(tc.doc))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want one naming %s", name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: the error repeats a value: %v", name, err)
		}
	}
}

// The schema's modes are the loader's.
func TestBusSchemaEnumMatchesTheModes(t *testing.T) {
	schema, err := schemas.Get("node.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	o, err := schemas.At(schema, "/$defs/bus")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{string(bus.ModeEmbedded), string(bus.ModeExternal)}
	if got := o.Enums["server"]; !slices.Equal(got, want) {
		t.Errorf("schema enum %v, modes %v", got, want)
	}
}

// The bus section is a host edit: nothing under it is on the config.propose
// allowlist.
func TestBusIsNotOnTheAllowlist(t *testing.T) {
	for _, p := range settings.AllowedPaths {
		if p == "bus" || strings.HasPrefix(p, "bus.") {
			t.Errorf("%s is on the allowlist", p)
		}
	}
	for _, p := range []string{"bus", "bus.server", "bus.urls", "bus.credentials", "bus.trust"} {
		if settings.Allowed(p) || settings.Where(p) != settings.PlaceHost {
			t.Errorf("%s: allowed %v, place %s", p, settings.Allowed(p), settings.Where(p))
		}
	}
}
