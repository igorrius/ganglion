package mcpadmin_test

import (
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/mcpadmin"
	"github.com/igorrius/ganglion/mcpclient"
	"github.com/igorrius/ganglion/settings"
)

// slowFile is a section whose first read waits until it is let go, the
// way a read can be overtaken by a later write and its Apply.
type slowFile struct {
	mcpadmin.File
	mu      sync.Mutex
	servers map[string]settings.MCPServer
	reads   int
	first   chan struct{} // closed when the first read has begun
	release chan struct{} // the first read returns after this is closed
	second  chan struct{} // closed when a second read begins
}

func (f *slowFile) MCP() (settings.MCP, error) {
	f.mu.Lock()
	f.reads++
	n, m := f.reads, settings.MCP{Servers: maps.Clone(f.servers)}
	f.mu.Unlock()
	switch n {
	case 1:
		close(f.first)
		<-f.release
	case 2:
		close(f.second)
	}
	return m, nil
}

func (f *slowFile) set(name string, s settings.MCPServer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.servers[name] = s
}

// setHub keeps the servers of the last Set.
type setHub struct {
	mu   sync.Mutex
	last []mcpclient.Server
}

func (h *setHub) Set(s []mcpclient.Server) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = s
}

func (*setHub) States() []mcpclient.Status { return nil }

// Two Applies at once leave the hub on the later file: an Apply that read
// before a write never hands the hub its older read after the later one
// (review of #271).
func TestApplyIsNeverOvertakenByAnOlderRead(t *testing.T) {
	f := &slowFile{servers: map[string]settings.MCPServer{}, first: make(chan struct{}), release: make(chan struct{}),
		second: make(chan struct{})}
	h := &setHub{}
	a := mcpadmin.New(mcpadmin.Deps{File: f, Hub: h, Keys: mcpadmin.StoreKeys{}})
	var wg sync.WaitGroup
	wg.Go(func() { _ = a.Apply() }) // reads the file without the server
	<-f.first
	f.set("shop", settings.MCPServer{URL: "https://mcp.example.com/mcp", Auth: settings.MCPAuthNone})
	wg.Go(func() { _ = a.Apply() }) // the change's own Apply
	// Without the lock the second read begins at once; with it, it waits for
	// the first Apply. Either way the first read is let go: at the second
	// read, or after a bound for the case where it cannot begin.
	select {
	case <-f.second:
	case <-time.After(200 * time.Millisecond):
	}
	close(f.release)
	wg.Wait()
	if len(h.last) != 1 || h.last[0].Name != "shop" {
		t.Fatalf("the hub was left on %+v", h.last)
	}
}
