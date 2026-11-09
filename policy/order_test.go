package policy

import (
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The rules run in this order, and the first that applies wins: a later edit
// cannot move the forbidden-command rule after the grant, where a grant would
// run a forbidden command (R-CMD-2).
func TestRulesRunInOrder(t *testing.T) {
	want := []string{"ruleDepth", "ruleAdminOnly", "ruleLocalOnly", "ruleForbidden", "ruleCommandForbidden", "ruleGrant",
		"ruleMemoryWrite", "ruleLanguageSet", "ruleHostExec", "rulePlanShow", "ruleSideEffect", "ruleTaintedInput", "ruleMessageSend", "ruleWebFetch"}
	got := make([]string, 0, len(rules))
	for _, r := range rules {
		name := runtime.FuncForPC(reflect.ValueOf(r).Pointer()).Name()
		got = append(got, name[strings.LastIndex(name, ".")+1:])
	}
	if !slices.Equal(got, want) {
		t.Fatalf("rules %v, want %v", got, want)
	}
}
