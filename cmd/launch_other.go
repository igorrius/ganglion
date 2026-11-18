//go:build !unix

package cmd

import "errors"

// execBinary is not available here: the node's host is Linux (ADR-0081).
func execBinary(string) error { return errors.New("update: exec needs a unix host") }

// fileOwner is not available here, so a host command never dispatches.
func fileOwner(string) (int, error) { return 0, errors.New("update: no file owner on this host") }
