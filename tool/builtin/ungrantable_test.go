package builtin

import "testing"

func TestLanguageSetCannotBeGranted(t *testing.T) {
	if !ungrantable(toolLanguageSet) {
		t.Fatal("a task can be granted a tool that needs a person")
	}
}

// A task may be granted host.exec: its read commands run there, and the
// tool refuses any other until a grant lists its exact commands (task 9).
func TestHostExecCanBeGranted(t *testing.T) {
	if ungrantable(HostExecTool) {
		t.Fatal("host.exec cannot be granted")
	}
}

// A module is imported, built and installed only with the owner at hand.
func TestModuleToolsCannotBeGranted(t *testing.T) {
	for _, name := range []string{ModuleImportTool, ModuleBuildTool, ModuleInstallTool} {
		if !ungrantable(name) {
			t.Fatalf("a task can be granted %s", name)
		}
	}
}
