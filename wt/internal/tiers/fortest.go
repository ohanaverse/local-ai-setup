package tiers

import (
	"os"
	"path/filepath"
)

// IsolateForTest points MODELTIERS_OUT at a file that does not exist, so a
// test binary that builds a model table never reads the developer's real
// tiers.toml (and never sees one MODELTIERS_OUT named in the shell the tests
// were run from). Nothing is created: the path is in a directory under the
// system temp directory that is never made. A test that wants a rating sets
// MODELTIERS_OUT itself (t.Setenv) to a file it wrote.
//
// It is called from the TestMain of every package whose tests build a model
// table or a claude launch (cmd/wt, internal/tui, internal/agents). Production code never calls it.
func IsolateForTest() {
	os.Setenv(OutEnv, filepath.Join(os.TempDir(), "wt-test-no-tiers-dir-never-created", "tiers.toml"))
}
