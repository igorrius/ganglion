package cells_test

import (
	"testing"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/cells/storetest"
)

func TestMemStore(t *testing.T) {
	storetest.Run(t, func(*testing.T) cells.Store { return cells.NewMemStore() })
}
