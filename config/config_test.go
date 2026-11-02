package config

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/core"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
	"github.com/samber/do/v2"
)

// Every combination of the optional parts builds, and shutting the
// container down releases the database: a wiring mistake fails here, not
// at start on a host.
func TestCoreContainerBuildsAndShutsDown(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	model := providertest.NewServer(t, "sk-test")
	catalog, err := rules.LoadCatalog(strings.NewReader("providers:\n  - {name: or, type: openai, base_url: " + model.URL() +
		", key_env: OR_KEY}\n  - {name: cli, type: claude-cli}\nmodels:\n  - {provider: or, name: m1, tier: smart, context: 64000}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		token, admin string
		agent        bool
		components   int
	}{
		"echo, no transport":  {components: 3},
		"echo over Telegram":  {token: testToken, admin: "telegram:user:42", components: 4},
		"agent over Telegram": {token: testToken, admin: "telegram:user:42", agent: true, components: 7},
		"agent, no transport": {agent: true, components: 6},
	} {
		t.Run(name, func(t *testing.T) {
			b := testConfig(t)
			b.BotToken, b.BotAPIURL, b.Admin = tc.token, fake.URL(), tc.admin
			if tc.agent {
				b.Agent = AgentSettings{Catalog: &catalog, Keys: map[string]string{"or": "sk-test"}, TZ: time.UTC}
			}
			for range 2 { // the second build proves the first released the lock
				i := New(context.Background(), b, slog.New(slog.DiscardHandler))
				comps, err := do.Invoke[[]node.Component](i)
				if err != nil {
					t.Fatal(err)
				}
				if len(comps) != tc.components {
					t.Fatalf("%d components, want %d", len(comps), tc.components)
				}
				if _, err := do.Invoke[core.Handler](i); err != nil {
					t.Fatal(err)
				}
				if rep := i.Shutdown(); !rep.Succeed {
					t.Fatalf("shutdown: %v", rep.Error())
				}
			}
		})
	}
}
