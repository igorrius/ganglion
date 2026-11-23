package settings

import (
	"net/url"
	"time"

	"github.com/igorrius/ganglion/bus"
)

// Edge is the settings of the edge role (R-EDGE-7): the node's name in the
// network, where the core's bus is, and how often it sends its telemetry.
// They count only for a node run with --roles edge. The section is
// host-only: it is not on config.propose's allowlist (R-CFG-14).
type Edge struct {
	// Name is the edge's name: its JetStream domain and the last token of
	// its subjects. Required for --roles edge.
	Name string `yaml:"name"`
	// URL is the core's bus for the leaf link: wss://<public address>/bus
	// (R-EDGE-4). "" runs the edge with no link.
	URL string `yaml:"url"`
	// Credentials is the path of the edge's .creds file; "" is edge.creds
	// beside the database.
	Credentials string `yaml:"credentials"`
	// TelemetryEvery is how often the edge sends its metrics (R-OBS-9).
	TelemetryEvery Duration `yaml:"telemetry_every"`
}

// DefaultTelemetryEvery is edge.telemetry_every when the file sets none.
const DefaultTelemetryEvery = 30 * time.Second

// minTelemetryEvery is the shortest edge.telemetry_every the node takes.
const minTelemetryEvery = 5 * time.Second

// check refuses an edge section that cannot run.
func (e Edge) check() error {
	if e.Name != "" && bus.CheckToken(e.Name) != nil {
		return pathError("edge.name", "must be one word: no dots, spaces, * or >")
	}
	if e.URL != "" {
		u, err := url.Parse(e.URL)
		if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "wss" && u.Scheme != "ws" && u.Scheme != "nats-leaf" && u.Scheme != "tls") {
			return pathError("edge.url", "must be the core's bus, such as wss://example.com/bus")
		}
	}
	if d := time.Duration(e.TelemetryEvery); d < minTelemetryEvery {
		return pathError("edge.telemetry_every", "must be 5s or more")
	}
	return nil
}
