package main

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// TestMain isolates the process from the developer's machine before any test
// runs: no config home, git repository, tiers file or model process may be
// reached from a wt-model test.
func TestMain(m *testing.M) {
	_, rmConfigHome := config.IsolateConfigHomeForTest()
	gitenv.IsolateForTest()
	tiers.IsolateForTest()
	lifecycle.IsolateProcessesForTest()
	code := m.Run()
	rmConfigHome()
	os.Exit(code)
}
