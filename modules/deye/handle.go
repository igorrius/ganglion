package deye

import (
	"errors"

	"github.com/igorrius/ganglion/pdk"
)

// Handle is the module's entry point: it reads the inverter named by the
// manifest's config.inverter_sn and returns the sample. Credential problems
// are reported as auth_failed so the runner escalates instead of retrying.
func Handle(h pdk.Host, _ pdk.Input) (any, error) {
	sn, ok := h.Config("inverter_sn")
	if !ok || sn == "" {
		return nil, errors.New("deye: config.inverter_sn is not set in the manifest")
	}
	s, err := Read(h, sn)
	if errors.Is(err, ErrAuth) {
		return nil, pdk.WithKind(pdk.KindAuthFailed, err)
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}
