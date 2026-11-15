package alert_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
)

func private(principal string) (string, string, bool) {
	if principal == "mqtt:user:9" {
		return "", "", false
	}
	return "telegram", "chat-of-" + principal, true
}

func TestMessagesGoToEveryAdminWithAPrivateChat(t *testing.T) {
	admins := alert.Admins{
		List: func(context.Context) ([]string, error) {
			return []string{"telegram:user:1", "mqtt:user:9", "telegram:user:2"}, nil
		},
		Private: private,
	}
	got, err := admins.Messages(context.Background(), "alert:x:7", "Something broke.")
	if err != nil {
		t.Fatal(err)
	}
	want := []event.Outgoing{
		{IdemKey: "alert:x:7:telegram:user:1", Transport: "telegram", Conv: "chat-of-telegram:user:1", Text: "Something broke."},
		{IdemKey: "alert:x:7:telegram:user:2", Transport: "telegram", Conv: "chat-of-telegram:user:2", Text: "Something broke."},
	}
	if len(got) != len(want) || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestMessagesReturnTheListError(t *testing.T) {
	boom := errors.New("db down")
	admins := alert.Admins{List: func(context.Context) ([]string, error) { return nil, boom }, Private: private}
	if _, err := admins.Messages(context.Background(), "k", "t"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the list error", err)
	}
}

func TestEachBuildsATextPerAdmin(t *testing.T) {
	admins := alert.Admins{
		List: func(context.Context) ([]string, error) {
			return []string{"telegram:user:1", "mqtt:user:9", "telegram:user:2"}, nil
		},
		Private: private,
	}
	got, err := admins.Each(context.Background(), "k", func(_ context.Context, p string) string { return "for " + p })
	if err != nil || len(got) != 2 || got[0].Text != "for telegram:user:1" || got[1].Text != "for telegram:user:2" || got[1].IdemKey != "k:telegram:user:2" {
		t.Fatalf("%+v %v", got, err)
	}
}
