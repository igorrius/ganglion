package settings

import (
	"fmt"

	"github.com/igorrius/ganglion/bus"
)

// Bus is the settings of the node's message bus (R-BUS-1). The section is
// host-only: it is not on config.propose's allowlist (R-CFG-14).
type Bus struct {
	// Server is embedded (the node runs nats-server in its process) or
	// external (it connects to one the operator runs).
	Server string `yaml:"server"`
	// URLs are the external server's addresses, nats:// or tls://: one
	// server or the members of a cluster. Required for external, refused for
	// embedded.
	URLs []string `yaml:"urls"`
	// Credentials is the text of a .creds file, as env:NAME or store:NAME.
	// Optional, and only for external.
	Credentials SecretRef `yaml:"credentials"`
}

// check refuses a bus section that cannot run. An error names the key and
// never repeats a value, which may hold a password.
func (b Bus) check() error {
	switch b.Mode() {
	case bus.ModeEmbedded:
		if len(b.URLs) > 0 {
			return pathError("bus.urls", "are for server: external; the embedded server opens no port")
		}
		if b.Credentials != "" {
			return pathError("bus.credentials", "are for server: external")
		}
	case bus.ModeExternal:
		return b.checkExternal()
	default:
		return pathError("bus.server", "must be one of: "+string(bus.ModeEmbedded)+", "+string(bus.ModeExternal))
	}
	return nil
}

// checkExternal refuses an external section without a usable address.
func (b Bus) checkExternal() error {
	if len(b.URLs) == 0 {
		return pathError("bus.urls", "must name the server, such as nats://nats.example.com:4222")
	}
	for i, u := range b.URLs {
		if bus.CheckURL(u) != nil {
			return pathError(fmt.Sprintf("bus.urls[%d]", i), "must be nats:// or tls:// with a host and no user or password")
		}
	}
	return nil
}

// Mode is the bus's mode as a named type.
func (b Bus) Mode() bus.Mode { return bus.Mode(b.Server) }

// CredentialsText reads the .creds text the section names through src; ""
// when the section names none. A reference that is not set is an error that
// says how to set it, so a node never starts believing it is signed in.
func (b Bus) CredentialsText(src Sources) (string, error) {
	if b.Credentials == "" {
		return "", nil
	}
	v, err := b.Credentials.Resolve(src)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", fmt.Errorf("%s: bus.credentials: %s is not set; %s", FileName, b.Credentials, b.Credentials.HowToSet())
	}
	return v, nil
}
