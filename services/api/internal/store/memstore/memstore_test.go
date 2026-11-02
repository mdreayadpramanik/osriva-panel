package memstore

import (
	"testing"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) store.Store { return New() })
}
