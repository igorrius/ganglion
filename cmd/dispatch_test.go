package cmd

import "testing"

func TestDBFromArgs(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	for _, tc := range []struct {
		args []string
		env  string
		want string
	}{
		{nil, "", "ganglion.db"},
		{nil, "/data/ganglion.db", "/data/ganglion.db"},
		{[]string{"now", "--db", "/x/a.db"}, "/data/ganglion.db", "/x/a.db"},
		{[]string{"list", "--db=/x/b.db"}, "", "/x/b.db"},
		{[]string{"set", "-db", "/x/c.db", "NAME"}, "", "/x/c.db"},
		{[]string{"set", "--module", "db", "NAME"}, "", "ganglion.db"},
		{[]string{"--", "--db", "/x/d.db"}, "", "ganglion.db"},
	} {
		if got := dbFromArgs(tc.args, env(tc.env)); got != tc.want {
			t.Errorf("%v %q: %q, want %q", tc.args, tc.env, got, tc.want)
		}
	}
}
