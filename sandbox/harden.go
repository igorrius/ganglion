package sandbox

// Harden marks this process not dumpable, as the sandbox requires (New turns
// the sandbox off for a dumpable process). The one place the node, chatlab and
// the tests do it; the caller says what to do with an error.
func Harden() error { return SetDumpable(false) }
