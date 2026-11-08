package hostcmd

import "testing"

func TestShow(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"rm", "-r", "my dir"}, "rm -r 'my dir'"},
		{[]string{"echo", "a'b"}, `echo 'a'\''b'`},
		{[]string{"echo", "a\nb"}, "echo 'a\nb'"},
		{[]string{"df", "-h"}, "df -h"},
	} {
		if got := Show(c.argv); got != c.want {
			t.Errorf("Show(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}
