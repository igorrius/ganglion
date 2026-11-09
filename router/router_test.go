package router_test

import (
	"testing"

	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/tool"
)

// Purposes and efforts are keys and values of ganglion.yaml, and purposes
// are metric and log values: their spelling must never change.
func TestVocabularyValues(t *testing.T) {
	for got, want := range map[string]string{
		string(router.PurposeChat): "chat", string(router.PurposeCheck): "check", string(router.PurposeCompact): "compact",
		string(router.PurposeTranslate): "translate", string(router.PurposeCode): "code",
		string(router.EffortLow): "low", string(router.EffortMedium): "medium", string(router.EffortHigh): "high",
		string(router.EffortXHigh): "xhigh", string(router.EffortMax): "max",
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
	if len(router.Purposes) != 5 || router.Purposes[0] != router.PurposeChat {
		t.Fatalf("Purposes = %v", router.Purposes)
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
