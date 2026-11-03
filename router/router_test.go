package router_test

import (
	"testing"

	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/tool"
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

func TestCandidateCostAtCataloguePrices(t *testing.T) {
	c := router.Candidate{PriceInMTok: 0.5, PriceOutMTok: 2}
	if got := c.Cost(2_000_000, 500_000); got != 2.0 { // 2M in at $0.5 + 0.5M out at $2
		t.Fatalf("Cost = %v, want 2.0", got)
	}
}

func TestLocalPlacesEveryToolHere(t *testing.T) {
	var p tool.Placement = router.Local{}
	for _, name := range []string{"message.send", "cells.set", ""} {
		if got := p.Where(name); got != tool.PlacementLocal {
			t.Errorf("Where(%q) = %q, want %q", name, got, tool.PlacementLocal)
		}
	}
}
