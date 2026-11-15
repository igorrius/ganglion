//go:build !unix

package update

import "errors"

// lockDir is not available here: the node's host is Linux (ADR-0081).
func lockDir(string) (func(), error) {
	return nil, errors.New("update: the release store needs a unix host")
}
