package update

import (
	"errors"

	"github.com/igorrius/ganglion/texts"
)

// Image is the project's published image, without a tag; a node that runs a
// build without a release version moves to Image:<version without v> once.
const Image = "ghcr.io/igorrius/ganglion"

// ImageRef is the image that holds the release v: Image, a colon and the
// version without its v.
func ImageRef(v Version) string {
	return Image + ":" + v.String()[1:]
}

// refusalTexts are the sentinels an update is refused with, in the order they
// are tried, each with its own words (R-UPD-2…5). Nothing in an error's own
// string is ever shown: it may hold what the network said.
var refusalTexts = []struct {
	err error
	key texts.Key
}{
	{ErrBadVersion, texts.UpdateRefusedBadVersion},
	{ErrNotNewer, texts.UpdateRefusedNotNewer},
	{ErrNotFound, texts.UpdateRefusedNotFound},
	{ErrNoPlatform, texts.UpdateRefusedNoPlatform},
	{ErrIncomplete, texts.UpdateRefusedIncomplete},
	{ErrBadSignature, texts.UpdateRefusedSignature},
	{ErrBadSums, texts.UpdateRefusedSums},
	{ErrSumMismatch, texts.UpdateRefusedMismatch},
	{ErrBadManifest, texts.UpdateRefusedManifest},
	{ErrEnv, texts.UpdateRefusedEnv},
	{ErrBusy, texts.UpdateRefusedBusy},
	{ErrFetch, texts.UpdateRefusedFetch},
	{ErrNoPrevious, texts.UpdateRefusedNoPrevious},
	{ErrBadState, texts.UpdateRefusedState},
}

// RefusalKey is the text that says why err refused an update or a
// rollback; an error that is none of the sentinels is the general one.
func RefusalKey(err error) texts.Key {
	for _, r := range refusalTexts {
		if errors.Is(err, r.err) {
			return r.key
		}
	}
	return texts.UpdateRefusedOther
}
