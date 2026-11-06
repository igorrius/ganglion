package builtin

import "testing"

func TestLanguageSetCannotBeGranted(t *testing.T) {
	if !ungrantable(toolLanguageSet) {
		t.Fatal("a task can be granted a tool that needs a person")
	}
}
