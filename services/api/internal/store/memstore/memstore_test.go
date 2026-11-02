package memstore

import (
	"testing"

	"github.com/osriva/osriva-panel/services/api/internal/store"
	"github.com/osriva/osriva-panel/services/api/internal/store/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) store.Store { return New() })
}
