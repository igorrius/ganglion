package host

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/status"
)

func TestTheSwitchIsOnUntilAnAdminTurnsItOff(t *testing.T) {
	ctx := context.Background()
	cs := cells.NewService(cells.NewMemStore())
	sw, err := NewSwitch(ctx, cs)
	if err != nil {
		t.Fatal(err)
	}
	if !sw.On(ctx) || !sw.Open() {
		t.Fatal("a new switch is off")
	}
	if got, _ := sw.Status(ctx, status.Scope{}); len(got) != 0 {
		t.Fatalf("an on switch has status lines: %+v", got)
	}
	if err := sw.Set(ctx, false, "telegram:user:1"); err != nil {
		t.Fatal(err)
	}
	if sw.On(ctx) || sw.Open() {
		t.Fatal("still on after off")
	}
	c, _ := cs.Get(ctx, SwitchCell)
	var v string
	_ = json.Unmarshal(c.Desired, &v)
	if v != "off" {
		t.Fatalf("cell = %s", c.Desired)
	}
	got, _ := sw.Status(ctx, status.Scope{})
	if len(got) != 1 || got[0].State != status.Off {
		t.Fatalf("status of an off switch: %+v", got)
	}
	if err := sw.Set(ctx, true, "telegram:user:1"); err != nil || !sw.On(ctx) {
		t.Fatalf("on again: %v", err)
	}
}

// brokenStore reads nothing once broken.
type brokenStore struct {
	*cells.MemStore
	broken bool
}

func (b *brokenStore) Get(ctx context.Context, id string) (cells.Cell, error) {
	if b.broken {
		return cells.Cell{}, errors.New("disk gone")
	}
	return b.MemStore.Get(ctx, id)
}

func TestASwitchThatCannotBeReadIsOff(t *testing.T) {
	ctx := context.Background()
	st := &brokenStore{MemStore: cells.NewMemStore()}
	sw, err := NewSwitch(ctx, cells.NewService(st))
	if err != nil {
		t.Fatal(err)
	}
	st.broken = true
	if sw.On(ctx) || sw.Open() {
		t.Fatal("an unreadable switch is on")
	}
}
