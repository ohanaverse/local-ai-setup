package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// The helpers below came with `wt-model model list` and its tests from
// cmd/wt-session (Task 37). modelHome, withCleanConfigEnv, mustRead and
// stubProbeInventory were already here (helpers_test.go).

// probeOf is an inventory probe that always sees snap: what runModelList is
// handed in a test in place of the host's probeInventory seam.
func probeOf(snap localmodels.Snapshot) func(*config.Config) localmodels.Snapshot {
	return func(*config.Config) localmodels.Snapshot { return snap }
}

// runWTModel runs `wt-model <args>` over the real root tree on a testHost,
// with stdout and stderr in one buffer, and returns it with the command's
// error.
func runWTModel(t *testing.T, args ...string) (string, error) {
	t.Helper()
	asWTModel(t)
	cmd := rootCmdWith(fakeDeps, testHost())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(t.Context())
	return buf.String(), err
}

// jsonKeySet returns a JSON object's top-level keys, sorted, or fails the test
// (copied from cmd/wt-session's litellm_test.go for the model list contract
// test).
func jsonKeySet(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, raw)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
