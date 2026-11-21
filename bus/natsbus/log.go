package natsbus

import (
	"fmt"
	"log/slog"
)

// slogLogger routes the embedded server's log to slog at warn and above;
// its notices, debug and trace lines are dropped. Fatalf logs at error
// and does not stop the process: the node decides that.
type slogLogger struct{ log *slog.Logger }

func (l slogLogger) Noticef(string, ...any) {}
func (l slogLogger) Debugf(string, ...any)  {}
func (l slogLogger) Tracef(string, ...any)  {}

func (l slogLogger) Warnf(format string, v ...any) {
	l.log.Warn("natsbus: server: " + fmt.Sprintf(format, v...))
}

func (l slogLogger) Errorf(format string, v ...any) {
	l.log.Error("natsbus: server: " + fmt.Sprintf(format, v...))
}

func (l slogLogger) Fatalf(format string, v ...any) { l.Errorf(format, v...) }
