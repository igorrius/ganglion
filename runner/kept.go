package runner

import (
	"context"
	"crypto/sha256"
	"sync"

	"github.com/tetratelabs/wazero"
)

// keptBuilds is how many recently loaded builds stay compiled after their
// last Close. Loading one of them again (the next example of a trial, a
// restart of a role) skips compilation; a build past them is freed, so
// builds compiled for trials and checks and never installed do not pile up
// (#219).
const keptBuilds = 4

// kept holds its own reference in compilationCache to each of the
// keptBuilds most recently loaded builds, newest first. wazero counts the
// references to a compiled build and drops its code when the last one is
// closed.
var kept = keptList{}

type keptBuild struct {
	id       [sha256.Size]byte
	compiled wazero.CompiledModule
}

type keptList struct {
	mu     sync.Mutex
	rt     wazero.Runtime // compiles the kept references; never closed
	builds []keptBuild
}

// keep makes wasm the most recently loaded build, compiling a reference to
// it (a cache hit: its loader has just compiled it) if it is not kept yet,
// and frees the oldest kept build past keptBuilds.
func (k *keptList) keep(ctx context.Context, wasm []byte) {
	id := sha256.Sum256(wasm)
	k.mu.Lock()
	defer k.mu.Unlock()
	for i, b := range k.builds {
		if b.id == id {
			copy(k.builds[1:i+1], k.builds[:i])
			k.builds[0] = b
			return
		}
	}
	if k.rt == nil {
		// The same flags as load: CloseOnContextDone is part of the cache's
		// key for a build, so a reference compiled without it would keep
		// another entry, not the one loads use.
		k.rt = wazero.NewRuntimeWithConfig(context.WithoutCancel(ctx), wazero.NewRuntimeConfig().
			WithCloseOnContextDone(true).
			WithCompilationCache(compilationCache))
	}
	compiled, err := k.rt.CompileModule(context.WithoutCancel(ctx), wasm)
	if err != nil {
		return // not kept: the next load compiles it again
	}
	k.builds = append([]keptBuild{{id: id, compiled: compiled}}, k.builds...)
	for len(k.builds) > keptBuilds {
		last := len(k.builds) - 1
		_ = k.builds[last].compiled.Close(ctx)
		k.builds = k.builds[:last]
	}
}

// forget frees every kept build.
func (k *keptList) forget(ctx context.Context) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, b := range k.builds {
		_ = b.compiled.Close(ctx)
	}
	k.builds = nil
}
