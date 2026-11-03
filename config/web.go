package config

import (
	"log/slog"

	"github.com/igorrius/ganglion/modules/html2md"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/web"
	"github.com/samber/do/v2"
)

// HTML2MD provides the html2md module embedded in the binary (ADR-0035);
// updates from the data volume come in M3c.
func HTML2MD() do.Provider[*runner.ToolModule] {
	return func(i do.Injector) (*runner.ToolModule, error) {
		return runner.LoadTool(do.MustInvoke[Startup](i), html2md.Wasm, html2md.Manifest,
			runner.Deps{Logger: do.MustInvoke[*slog.Logger](i)})
	}
}

// Fetcher provides web.fetch's reader with the default limits, HTML
// converted by html2md.
func Fetcher() do.Provider[*web.Fetcher] {
	return func(i do.Injector) (*web.Fetcher, error) {
		return web.NewFetcher(web.DefaultFetchConfig(), web.NewModuleConverter(do.MustInvoke[*runner.ToolModule](i))), nil
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
