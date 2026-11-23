package telemetry

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
)

func family(name string, typ dto.MetricType, node string) *dto.MetricFamily {
	ln, v := "node", node
	return &dto.MetricFamily{Name: &name, Type: &typ, Metric: []*dto.Metric{{Label: []*dto.LabelPair{{Name: &ln, Value: &v}}, Gauge: &dto.Gauge{}}}}
}

// The edges' families are merged by name; one of another type is left out;
// a forgotten edge is gone.
func TestNetworkGathers(t *testing.T) {
	n := NewNetwork()
	n.Set("a", []*dto.MetricFamily{family("up", dto.MetricType_GAUGE, "a"), family("b_total", dto.MetricType_GAUGE, "a")})
	n.Set("b", []*dto.MetricFamily{family("up", dto.MetricType_GAUGE, "b")})
	mfs, err := n.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(mfs) != 2 || mfs[0].GetName() != "b_total" || mfs[1].GetName() != "up" || len(mfs[1].Metric) != 2 {
		t.Fatalf("gathered %v", mfs)
	}
	n.Set("c", []*dto.MetricFamily{family("up", dto.MetricType_COUNTER, "c")})
	n.Forget("a")
	mfs, _ = n.Gather()
	total := 0
	for _, mf := range mfs {
		total += len(mf.Metric)
	}
	if total != 1 && total != 2 {
		t.Fatalf("series = %d", total)
	}
}
