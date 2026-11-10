// Package size parses byte sizes written as "8MiB", "64KiB", "1GiB" or
// plain bytes, the one form manifests and the node config share.
package size

import (
	"errors"
	"strconv"
	"strings"
)

var units = []struct {
	suffix string
	mult   uint64
}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}}

// Parse reads s; a bare number is bytes.
func Parse(s string) (uint64, error) {
	num, mult := s, uint64(1)
	for _, u := range units {
		if rest, ok := strings.CutSuffix(s, u.suffix); ok {
			num, mult = rest, u.mult
			break
		}
	}
	n, err := strconv.ParseUint(num, 10, 64)
	if err != nil {
		return 0, errors.New("size " + strconv.Quote(s) + ": want a whole number of bytes, KiB, MiB or GiB")
	}
	if n > ^uint64(0)/mult {
		return 0, errors.New("size " + strconv.Quote(s) + ": too large")
	}
	return n * mult, nil
}
