package sandbox

import (
	"context"
	"strconv"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// offReasons are the reasons' texts in /status.
var offReasons = map[Reason]texts.Key{
	NotLinux: texts.StatusReasonSandboxNotLinux, Root: texts.StatusReasonSandboxRoot,
	NoLandlock: texts.StatusReasonSandboxLandlock, Dumpable: texts.StatusReasonSandboxDumpable,
	SelfTestFailed: texts.StatusReasonSandboxSelfTest,
}

// Name implements status.Source.
func (*Sandbox) Name() string { return "sandbox" }

// Status implements status.Source: ready with Landlock's version, or off
// with its reason. Off is not a warning: a node may run without commands.
func (s *Sandbox) Status(context.Context, status.Scope) ([]status.Check, error) {
	if s.reason == Ready {
		return []status.Check{{Section: status.SectionProblems, State: status.OK, Line: texts.StatusSandboxReady,
			Vars: texts.Vars{"abi": strconv.Itoa(s.abi)}}}, nil
	}
	return []status.Check{{Section: status.SectionProblems, State: status.Off, Line: texts.StatusSandboxOff,
		Reason: offReasons[s.reason], Since: s.since}}, nil
}
