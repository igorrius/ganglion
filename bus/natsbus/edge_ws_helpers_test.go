package natsbus_test

import (
	"net/url"
	"os"
)

type urlT = url.URL

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }

func writeFile(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }
