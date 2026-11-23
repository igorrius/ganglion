package settings

import "testing"

func TestEdgeCheck(t *testing.T) {
	ok := Edge{Name: "garage", URL: "wss://example.com/bus", TelemetryEvery: Duration(DefaultTelemetryEvery)}
	if err := ok.check(); err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]Edge{
		"the core's name": {Name: "core", TelemetryEvery: ok.TelemetryEvery},
		"two words":       {Name: "a.b", TelemetryEvery: ok.TelemetryEvery},
		"not a bus url":   {Name: "garage", URL: "https://example.com/", TelemetryEvery: ok.TelemetryEvery},
		"too often":       {Name: "garage", TelemetryEvery: Duration(DefaultTelemetryEvery / 30)},
	} {
		if err := e.check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
