package telegram

import "testing"

func TestConvRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		chat, thread int64
		key          string
	}{
		{123456, 0, "telegram:123456"},
		{-1001234567890, 0, "telegram:-1001234567890"},
		{-1001234567890, 45, "telegram:-1001234567890/45"},
	} {
		if got := ConvKey(tc.chat, tc.thread); got != tc.key {
			t.Errorf("ConvKey(%d, %d) = %q, want %q", tc.chat, tc.thread, got, tc.key)
		}
		chat, thread, err := ParseConv(tc.key)
		if err != nil || chat != tc.chat || thread != tc.thread {
			t.Errorf("ParseConv(%q) = %d, %d, %v", tc.key, chat, thread, err)
		}
	}
}

func TestParseConvRejects(t *testing.T) {
	for _, bad := range []string{
		"", "telegram:", "mqtt:123", "telegram:abc", "telegram:0",
		"telegram:-100/", "telegram:-100/0", "telegram:-100/-4", "telegram:-100/4/5", "telegram:12 ",
	} {
		if _, _, err := ParseConv(bad); err == nil {
			t.Errorf("ParseConv(%q) succeeded", bad)
		}
	}
}

func TestPrincipal(t *testing.T) {
	if got := Principal(123456); got != "telegram:user:123456" {
		t.Fatalf("Principal = %q", got)
	}
}

func TestPrivateConvOfAPrincipal(t *testing.T) {
	for _, tc := range []struct {
		principal string
		conv      string
		ok        bool
	}{
		{Principal(42), ConvKey(42, 0), true},
		{"telegram:user:x", "", false},
		{"mqtt:user:42", "", false},
		{"", "", false},
	} {
		transport, conv, ok := PrivateConv(tc.principal)
		if ok != tc.ok || conv != tc.conv || (ok && transport != Name) {
			t.Errorf("PrivateConv(%q) = %q, %q, %v", tc.principal, transport, conv, ok)
		}
	}
}

func TestGroupKeyAndChat(t *testing.T) {
	if got := GroupKey(-1001); got != "telegram:-1001" {
		t.Fatalf("GroupKey = %q", got)
	}
	if chat, ok := GroupChat("telegram:-1001"); !ok || chat != -1001 {
		t.Fatalf("GroupChat = %d, %v", chat, ok)
	}
	for _, key := range []string{"telegram:42", "telegram:-1001/45", "matrix:-1001", "x", ""} {
		if _, ok := GroupChat(key); ok {
			t.Errorf("GroupChat(%q) is a group", key)
		}
	}
}

func TestParseLink(t *testing.T) {
	for link, want := range map[string]string{
		"https://t.me/c/1234567890/8":          "telegram:-1001234567890/8",
		"https://t.me/c/1234567890/8/1234":     "telegram:-1001234567890/8",
		" https://t.me/c/1234567890/8?single ": "telegram:-1001234567890/8",
		"https://t.me/c/1234567890/1":          "telegram:-1001234567890",
	} {
		if got, ok := ParseLink(link); !ok || got != want {
			t.Errorf("ParseLink(%q) = %q, %v; want %q", link, got, ok, want)
		}
	}
	for _, link := range []string{"", "K7", "https://t.me/family_chat/8", "https://t.me/c/1234567890", "https://evil.example/c/1234567890/8",
		"https://t.me/c/1234567890/8/9/10", "https://t.me/c/abc/8", "https://t.me/c/1234567890/8 and more", "telegram:-1001/8"} {
		if got, ok := ParseLink(link); ok {
			t.Errorf("ParseLink(%q) = %q: not a link to a topic", link, got)
		}
	}
}
