package config

import (
	"errors"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/modstore/release"
	"github.com/igorrius/ganglion/modules/html2md"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/web"
	"github.com/samber/do/v2"
)

// Modules provides the module store with html2md, the built-in module
// whose updates come from the project's releases (R-RUN-8, ADR-0041).
func Modules() do.Provider[*modstore.Store] {
	return func(i do.Injector) (*modstore.Store, error) {
		return modstore.Open(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, do.MustInvoke[Bootstrap](i).ModulesDir,
			time.Now, runner.Deps{Logger: do.MustInvoke[*slog.Logger](i)},
			modstore.Builtin{Name: html2mdName, Wasm: html2md.Wasm, Manifest: html2md.Manifest, SelfTest: html2md.SelfTest,
				Ceiling: modstore.Ceiling{ABI: html2md.ABI, MaxMemory: html2md.MaxMemory, MaxTimeout: html2md.MaxTimeout}})
	}
}

// html2mdName is the built-in module's name, in the store and its manifest.
const html2mdName = "html2md"

// ModuleAdmin provides what /modules drives: the store and the project's
// release channel.
func ModuleAdmin() do.Provider[agent.Modules] {
	return func(i do.Injector) (agent.Modules, error) {
		return modstore.Admin{Store: do.MustInvoke[*modstore.Store](i), Source: release.NewGitHub(release.ProjectRepo)}, nil
	}
}

// HTML2MD provides html2md as the store switches it, compiled on the
// first HTML page (ADR-0035).
func HTML2MD() do.Provider[*runner.SwapTool] {
	return func(i do.Injector) (*runner.SwapTool, error) {
		t, ok := do.MustInvoke[*modstore.Store](i).Tool(html2mdName)
		if !ok {
			return nil, errors.New("config: no html2md in the module store")
		}
		return t, nil
	}
}

// Fetcher provides web.fetch's reader with the default limits, HTML
// converted by html2md.
func Fetcher() do.Provider[*web.Fetcher] {
	return func(i do.Injector) (*web.Fetcher, error) {
		return web.NewFetcher(web.DefaultFetchConfig(), web.NewModuleConverter(do.MustInvoke[*runner.SwapTool](i))), nil
	}
}

// BraveSearch provides the Brave backend with the key from the settings.
func BraveSearch() do.Provider[web.Searcher] {
	return func(i do.Injector) (web.Searcher, error) {
		return web.NewBrave(do.MustInvoke[Bootstrap](i).Agent.BraveKey), nil
	}
}

// DuckDuckGoSearch provides the key-free backend.
func DuckDuckGoSearch() do.Provider[web.Searcher] {
	return func(do.Injector) (web.Searcher, error) { return web.NewDuckDuckGo(), nil }
}

// Reading is the web tools the agent has: web.fetch always, web.search
// when a backend is on.
type Reading struct {
	Tools []tool.Handler
}

// Read provides the web tools. The search backend is Brave with a key,
// DuckDuckGo without one; GANGLION_SEARCH=off leaves web.search out.
func Read() do.Provider[Reading] {
	return func(i do.Injector) (Reading, error) {
		tools := []tool.Handler{builtin.NewWebFetch(do.MustInvoke[*web.Fetcher](i))}
		b := do.MustInvoke[Bootstrap](i)
		if b.Agent.SearchOff {
			return Reading{Tools: tools}, nil
		}
		backend := searchDuckDuckGo
		if b.Agent.BraveKey != "" {
			backend = searchBrave
		}
		return Reading{Tools: append(tools, builtin.NewWebSearch(do.MustInvokeNamed[web.Searcher](i, backend)))}, nil
	}
}
