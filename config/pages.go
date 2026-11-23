package config

import (
	"log/slog"

	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/modhttp"
	"github.com/igorrius/ganglion/node"
	"github.com/samber/do/v2"
)

// pageListener is the loopback listener of modules' pages (R-HTTP-1,
// R-EDGE-7): a component of a node with the edge role, none when
// http.local_addr is off or the node has no edge role.
func pageListener(i do.Injector) ([]node.Component, error) {
	b := do.MustInvoke[Bootstrap](i)
	if !b.ServesPages || !b.Settings.HTTP.LocalOn() {
		return nil, nil
	}
	l, err := modhttp.New(modhttp.DefaultConfig(b.Settings.HTTP.LocalAddr), do.MustInvoke[*modhost.Host](i), do.MustInvoke[*slog.Logger](i))
	if err != nil {
		return nil, err
	}
	return []node.Component{l}, nil
}
