package runner

import (
	"reflect"
	"unsafe"
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
