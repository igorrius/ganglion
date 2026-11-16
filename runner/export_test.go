package runner

import (
	"context"
	"reflect"
	"unsafe"

	"github.com/tetratelabs/wazero"
)

// CompiledModules is the number of compiled modules the shared
// compilationCache holds, over all its engines. wazero keeps it unexported,
// so the test reads it through reflection.
func CompiledModules() uint32 {
	type counter interface{ CompiledModuleCount() uint32 }
	engs := reflect.ValueOf(compilationCache).Elem().FieldByName("engs")
	var n uint32
	for i := range engs.Len() {
		f := engs.Index(i)
		f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
		if f.IsNil() {
			continue
		}
		n += f.Interface().(counter).CompiledModuleCount()
	}
	return n
}

// KeptBuilds is how many recently loaded builds stay compiled.
const KeptBuilds = keptBuilds

// ForgetKept frees the builds the runner keeps compiled.
func ForgetKept() { kept.forget(context.Background()) }

// KeepsBuildOf reports whether the most recently kept build is the same
// cache entry mod was loaded from: the cache's key for a build includes
// runtime flags, so a reference compiled with other flags keeps nothing a
// load uses.
func KeepsBuildOf(mod *Module) bool {
	kept.mu.Lock()
	defer kept.mu.Unlock()
	return len(kept.builds) > 0 && cacheKey(kept.builds[0].compiled) == cacheKey(mod.compiled)
}

func cacheKey(c wazero.CompiledModule) [32]byte {
	f := reflect.ValueOf(c).Elem().FieldByName("module").Elem().FieldByName("ID")
	return reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Interface().([32]byte)
}
