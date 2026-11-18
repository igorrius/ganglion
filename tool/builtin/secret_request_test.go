package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

// fakeAsker records the request it was given.
type fakeAsker struct {
	asks []intake.SecretAsk
	err  error
}

func (f *fakeAsker) Ask(_ context.Context, k intake.SecretAsk, _ intake.SecretDoneFunc) (intake.Asked, error) {
	if f.err != nil {
		return intake.Asked{}, f.err
	}
	f.asks = append(f.asks, k)
	return intake.Asked{ID: "id1", Expires: time.Date(2026, 11, 18, 12, 10, 0, 0, time.UTC)}, nil
}

func manifestOf(m runner.Manifest) builtin.ManifestFunc {
	return func(module string) (runner.Manifest, error) {
		if module != "shop" {
			return runner.Manifest{}, errors.New("no such module")
		}
		return m, nil
	}
}

func shopManifest() runner.Manifest {
	var m runner.Manifest
	m.Capabilities.SecretGet = []string{"SHOP_KEY", "WIFI_PASSWORD"}
	m.Capabilities.HTTPFetch = []string{"b.example.com", "a.example.com", "b.example.com"}
	return m
}

func secretCall(args string) tool.Call {
	return tool.Call{TurnID: "turn1", N: 2, Conv: "telegram:42", Transport: "telegram", Principal: "telegram:user:1",
		Admin: true, Args: json.RawMessage(args)}
}

func TestSecretRequestSpec(t *testing.T) {
	s := builtin.NewSecretRequest(&fakeAsker{}, builtin.NoModuleSources, nil).Spec()
	if s.Name != builtin.SecretRequestTool || s.Class != tool.ClassSideEffect || s.Policy != tool.PolicyAuto || !s.AdminOnly {
		t.Fatalf("spec %+v", s)
	}
}

func TestSecretRequestNodeScope(t *testing.T) {
	fa := &fakeAsker{}
	h := builtin.NewSecretRequest(fa, builtin.NoModuleSources, []string{"BOT_TOKEN"})
	c := secretCall(`{"name":"WIFI_PASSWORD","why":"  the\nrouter  "}`)
	c.Tainted = true
	res, err := h.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	want := intake.SecretAsk{Scope: secrets.ScopeNode, Name: "WIFI_PASSWORD", Asking: intake.AskingNode, Why: "the router",
		Admin: "telegram:user:1", Transport: "telegram", Conv: "telegram:42", Key: "turn1:2", Tainted: true}
	if len(fa.asks) != 1 || fa.asks[0].Scope != want.Scope || fa.asks[0].Name != want.Name || fa.asks[0].Asking != want.Asking ||
		fa.asks[0].Why != want.Why || fa.asks[0].Admin != want.Admin || fa.asks[0].Transport != want.Transport ||
		fa.asks[0].Conv != want.Conv || fa.asks[0].Key != want.Key || !fa.asks[0].Tainted || len(fa.asks[0].Hosts) != 0 {
		t.Fatalf("ask %+v, want %+v", fa.asks, want)
	}
	if !strings.Contains(res.Text, "WIFI_PASSWORD") || !strings.Contains(res.Text, string(secrets.ScopeNode)) || res.External {
		t.Fatalf("result %+v", res)
	}
}

func TestSecretRequestModuleScope(t *testing.T) {
	fa := &fakeAsker{}
	h := builtin.NewSecretRequest(fa, manifestOf(shopManifest()), []string{"BOT_TOKEN"})
	if _, err := h.Run(context.Background(), secretCall(`{"name":"SHOP_KEY","why":"orders","module":"shop"}`)); err != nil {
		t.Fatal(err)
	}
	k := fa.asks[0]
	if k.Scope != secrets.ModuleScope("local/shop") || k.Asking != intake.AskingModule || k.Who != "shop" ||
		strings.Join(k.Hosts, ",") != "a.example.com,b.example.com" {
		t.Fatalf("ask %+v", k)
	}
}

func TestSecretRequestRefusals(t *testing.T) {
	long := strings.Repeat("я", 301)
	h := builtin.NewSecretRequest(&fakeAsker{}, manifestOf(shopManifest()), []string{"BOT_TOKEN"})
	for name, tc := range map[string]struct {
		h    *builtin.SecretRequest
		call tool.Call
	}{
		"not in the manifest": {h, secretCall(`{"name":"OTHER","why":"x","module":"shop"}`)},
		"node secret":         {h, secretCall(`{"name":"BOT_TOKEN","why":"x","module":"shop"}`)},
		"node secret file":    {h, secretCall(`{"name":"BOT_TOKEN_FILE","why":"x","module":"shop"}`)},
		"bad name":            {h, secretCall(`{"name":"a b","why":"x"}`)},
		"empty why":           {h, secretCall(`{"name":"A","why":"  "}`)},
		"long why":            {h, secretCall(`{"name":"A","why":"` + long + `"}`)},
		"unknown argument":    {h, secretCall(`{"name":"A","why":"x","value":"hunter2"}`)},
		"bad module name":     {h, secretCall(`{"name":"A","why":"x","module":"../x"}`)},
		"unknown module":      {h, secretCall(`{"name":"A","why":"x","module":"nope"}`)},
		"no module sources":   {builtin.NewSecretRequest(&fakeAsker{}, builtin.NoModuleSources, nil), secretCall(`{"name":"A","why":"x","module":"shop"}`)},
		"scheduled run":       {h, func() tool.Call { c := secretCall(`{"name":"A","why":"x"}`); c.Grant = &tool.Grant{}; return c }()},
	} {
		t.Run(name, func(t *testing.T) {
			fa := &fakeAsker{}
			hh := builtin.NewSecretRequest(fa, manifestOf(shopManifest()), []string{"BOT_TOKEN"})
			if tc.h != h {
				hh = tc.h
			}
			if _, err := hh.Run(context.Background(), tc.call); err == nil {
				t.Fatal("no error")
			}
			if len(fa.asks) != 0 {
				t.Fatalf("asked %+v", fa.asks)
			}
		})
	}
}

func TestSecretRequestAskErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"page changed":   {intake.ErrPageChanged, "failed its check"},
		"page unchecked": {intake.ErrPageUnchecked, "failed its check"},
		"too many":       {intake.ErrTooMany, "too many requests"},
	} {
		t.Run(name, func(t *testing.T) {
			h := builtin.NewSecretRequest(&fakeAsker{err: tc.err}, builtin.NoModuleSources, nil)
			_, err := h.Run(context.Background(), secretCall(`{"name":"A","why":"x"}`))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
		})
	}
}
