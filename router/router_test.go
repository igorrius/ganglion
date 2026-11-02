package router_test

import (
	"testing"

	"github.com/igorrius/ganglion/router"
)

// Tiers come from the model catalogue (YAML); task kinds are metric and
// log values: their spelling must never change.
func TestVocabularyValues(t *testing.T) {
	for got, want := range map[string]string{
		string(router.TierCheap): "cheap", string(router.TierSmart): "smart",
		string(router.TaskMessage): "message", string(router.TaskCompaction): "compaction",
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
}
