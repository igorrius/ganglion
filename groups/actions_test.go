package groups_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/groups"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
)

// file records the lines written, and fails when told to.
type file struct {
	set  []settings.Group
	fail error
}

func (f *file) SetGroup(g settings.Group) error {
	if f.fail != nil {
		return f.fail
	}
	f.set = append(f.set, g)
	return nil
}

// chatOf is telegram.GroupChat for the keys of these tests.
func chatOf(group string) (int64, bool) {
	if group == family {
		return -1001, true
	}
	return 0, false
}

func newActions(s *groups.Store, f *file, isAdmin bool) groups.Actions {
	return groups.Actions{Store: s, File: f, Chat: chatOf,
		IsAdmin: func(_ context.Context, p string) (bool, error) { return isAdmin && p == admin, nil }}
}

func press(t *testing.T, action string, a groups.ButtonArgs) pending.QuickPress {
	t.Helper()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return pending.QuickPress{Speak: texts.Speaker{Lang: texts.English},
		Button: pending.Button{ID: "B1", Action: action, Principal: admin, Conv: "telegram:42", Args: raw}, Event: "ev1"}
}

// Allow writes the group's line with the role family and forgets the request.
func TestAllowConnectsTheGroup(t *testing.T) {
	ctx := context.Background()
	s, f := newStore(t, &clock{at: t0}), &file{}
	if err := s.Set(ctx, family, "Family", groups.StatusPending); err != nil {
		t.Fatal(err)
	}
	allow := newActions(s, f, true).QuickActions()[groups.ActionAllow]
	p := press(t, groups.ActionAllow, groups.ButtonArgs{Group: family, Title: "Family *one*"})
	for range 2 { // a second press is harmless
		edit, err := allow(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if len(edit.Buttons) != 0 || !strings.Contains(edit.Text, `Family \*one\*`) {
			t.Fatalf("edit %+v", edit)
		}
		if !strings.Contains(edit.Note, family) || strings.Contains(edit.Note, "Family") {
			t.Fatalf("the note for the model must name the key and never the title: %q", edit.Note)
		}
	}
	want := settings.Group{Chat: -1001, Name: "Family *one*", Role: settings.GroupRoleFamily}
	if len(f.set) != 2 || f.set[0] != want || f.set[1] != want {
		t.Fatalf("written %+v", f.set)
	}
	if _, ok := status(t, s, family); ok {
		t.Fatal("the request is still stored after Allow")
	}
}

// Refuse remembers the refusal and writes nothing.
func TestRefuseIsRemembered(t *testing.T) {
	ctx := context.Background()
	s, f := newStore(t, &clock{at: t0}), &file{}
	edit, err := newActions(s, f, true).QuickActions()[groups.ActionRefuse](ctx, press(t, groups.ActionRefuse, groups.ButtonArgs{Group: family, Title: "Family"}))
	if err != nil || len(edit.Buttons) != 0 || edit.Text == "" || len(f.set) != 0 {
		t.Fatalf("edit %+v, err %v, written %+v", edit, err, f.set)
	}
	if st, ok := status(t, s, family); !ok || st != groups.StatusRefused {
		t.Fatalf("status %q %v", st, ok)
	}
}

// Abuse: a press by someone who is no longer an admin, and arguments that
// name no group, change nothing.
func TestAllowRefusesWhatItMustNotDo(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		isAdmin bool
		args    groups.ButtonArgs
	}{
		"not an admin any more": {false, groups.ButtonArgs{Group: family, Title: "Family"}},
		"a private chat's key":  {true, groups.ButtonArgs{Group: "telegram:42"}},
		"a topic's key":         {true, groups.ButtonArgs{Group: family + "/45"}},
		"no group":              {true, groups.ButtonArgs{}},
	} {
		s, f := newStore(t, &clock{at: t0}), &file{}
		if err := s.Set(ctx, family, "Family", groups.StatusPending); err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{groups.ActionAllow, groups.ActionRefuse} {
			if _, err := newActions(s, f, tc.isAdmin).QuickActions()[action](ctx, press(t, action, tc.args)); err == nil {
				t.Errorf("%s: %s = nil, want an error", name, action)
			}
		}
		if st, _ := status(t, s, family); len(f.set) != 0 || st != groups.StatusPending {
			t.Errorf("%s: written %+v, status %q", name, f.set, st)
		}
	}
}

// A group's title is its own text: what goes into the file is cut.
func TestAllowCutsALongTitle(t *testing.T) {
	ctx := context.Background()
	s, f := newStore(t, &clock{at: t0}), &file{}
	p := press(t, groups.ActionAllow, groups.ButtonArgs{Group: family, Title: strings.Repeat("я", 300)})
	if _, err := newActions(s, f, true).QuickActions()[groups.ActionAllow](ctx, p); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(f.set[0].Name)); n > 65 {
		t.Fatalf("the written name has %d runes", n)
	}
}

// A line that could not be written leaves the request waiting.
func TestAllowKeepsTheRequestWhenTheFileFails(t *testing.T) {
	ctx := context.Background()
	s, f := newStore(t, &clock{at: t0}), &file{fail: errors.New("disk full")}
	if err := s.Set(ctx, family, "Family", groups.StatusPending); err != nil {
		t.Fatal(err)
	}
	if _, err := newActions(s, f, true).QuickActions()[groups.ActionAllow](ctx, press(t, groups.ActionAllow, groups.ButtonArgs{Group: family})); err == nil {
		t.Fatal("Allow = nil with a failing file")
	}
	if st, ok := status(t, s, family); !ok || st != groups.StatusPending {
		t.Fatalf("status %q %v", st, ok)
	}
}
