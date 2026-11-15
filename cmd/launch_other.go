//go:build !unix

package cmd

import "errors"

// execBinary is not available here: the node's host is Linux (ADR-0081).
func execBinary(string) error { return errors.New("update: exec needs a unix host") }
