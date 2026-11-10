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

// A module is imported, built, installed and written only with the owner at
// hand; the reads module.status and module.read may be granted (owner,
// 2026-11-10).
func TestModuleToolsGrantRules(t *testing.T) {
	for _, name := range []string{ModuleImportTool, ModuleBuildTool, ModuleInstallTool, ModuleWriteTool} {
		if !ungrantable(name) {
			t.Fatalf("a task can be granted %s", name)
		}
	}
	for _, name := range []string{ModuleStatusTool, ModuleReadTool} {
		if ungrantable(name) {
			t.Fatalf("a task cannot be granted %s", name)
		}
	}
}
