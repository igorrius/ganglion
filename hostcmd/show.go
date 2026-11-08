package hostcmd

import (
	"regexp"
	"strings"
)

var plainArg = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// Show is argv as one line a person reads in a question, a plan or an
// alert: plain arguments as they are, others in single quotes, so a space
// or a quote inside an argument cannot make it look like two.
func Show(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if plainArg.MatchString(a) {
			parts[i] = a
			continue
		}
		parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " ")
}
