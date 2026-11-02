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
