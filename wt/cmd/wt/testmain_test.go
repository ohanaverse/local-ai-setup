package main

import (
	"errors"
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestMain stubs the two live seams a cmd/wt test would otherwise hit: the
// local-model inventory probe (no test may dial the developer's real
// ollama/omlx/mtplx servers) and the non-TUI start driver (no test may start
// a real model process). Tests that need a probe result call
// stubProbeInventory; tests that exercise a start call stubStartDriver.
func TestMain(m *testing.M) {
	probeInventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }
	startModel = func(*config.Config, catalog.Row, bool) error {
		return errors.New("startModel not stubbed in this test")
	}
	os.Exit(m.Run())
}

// startRequest records what a stubbed start driver was asked to do.
type startRequest struct {
	called  bool
	row     catalog.Row
	replace bool
}

// stubProbeInventory makes the non-TUI paths see snap for the duration of a
// test; the seam is restored on cleanup.
func stubProbeInventory(t *testing.T, snap localmodels.Snapshot) {
	t.Helper()
	old := probeInventory
	probeInventory = func(*config.Config) localmodels.Snapshot { return snap }
	t.Cleanup(func() { probeInventory = old })
}

// stubStartDriver replaces the driver with a recorder returning err, so a
// test can assert which row was started and with which replace permission
// without invoking the lifecycle engine.
func stubStartDriver(t *testing.T, err error) *startRequest {
	t.Helper()
	req := &startRequest{}
	old := startModel
	startModel = func(_ *config.Config, row catalog.Row, replace bool) error {
		req.called, req.row, req.replace = true, row, replace
		return err
	}
	t.Cleanup(func() { startModel = old })
	return req
}
