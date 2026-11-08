package hostcmd

import "testing"

func TestShow(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"rm", "-r", "my dir"}, "rm -r 'my dir'"},
		{[]string{"echo", "a'b"}, `echo 'a'\''b'`},
		{[]string{"echo", "a\nb"}, `echo $'a\nb'`},
		{[]string{"echo", "a\u202eb"}, `echo $'a\u202eb'`},
		{[]string{"echo", "a\u200bb"}, `echo $'a\u200bb'`},
		{[]string{"echo", "a\x7fb\x00"}, `echo $'a\x7fb\x00'`},
		{[]string{"echo", "a\u00a0b"}, `echo $'a\u00a0b'`},
		{[]string{"echo", "a\xffb"}, `echo $'a\xffb'`},
		{[]string{"echo", "it's\n"}, `echo $'it\'s\n'`},
		{[]string{"echo", "привіт світ"}, "echo 'привіт світ'"},
		{[]string{"echo", "привіт"}, "echo 'привіт'"},
		{[]string{"df", "-h"}, "df -h"},
	} {
		if got := Show(c.argv); got != c.want {
			t.Errorf("Show(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}
