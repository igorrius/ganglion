package deyeref

import (
	"errors"

	"github.com/igorrius/ganglion/pdk"
)

// Handle is the reference module's entry point: on cron and on its status
// tool it reads the inverter named by the config key inverter_sn and returns
// the sample. Credential problems are reported as auth_failed. Any other
// trigger, init and health among them, is unknown, so loading the module
// makes no cloud call.
func Handle(h pdk.Host, in pdk.Input) (any, error) {
	if in.Trigger != "cron" && in.Trigger != "tool:status" {
		return nil, errors.New(pdk.ErrUnknownTrigger)
	}
	sn, ok := h.Config("inverter_sn")
	if !ok || sn == "" {
		return nil, errors.New("deye: config.inverter_sn is not set")
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
