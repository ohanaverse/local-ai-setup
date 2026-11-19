package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// The helpers below came with `wt-model model list` (Task 37) and
// `wt-model model init|add|edit|rm` (Task 38) and their tests from
// cmd/wt-session. modelHome, withCleanConfigEnv, mustRead, writeRegistry,
// redirectedRegistry, onPath and the stub* helpers of the host's other seams
// were already here (helpers_test.go).

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
	return runWTModelOn(t, testHost(), args...)
}

// runWTModelOn is runWTModel over host h, for a test that stubs h's seams
// first. Each call is a new command line, as a new process would be: h's
// cached host config is dropped first, so a run after one that wrote the
// registry reads the registry as it is now.
func runWTModelOn(t *testing.T, h *host, args ...string) (string, error) {
	t.Helper()
	asWTModel(t)
	h.hostSet = false
	cmd := rootCmdWith(fakeDeps, h)
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

// stubConfirmRemove answers h's `wt-model model rm` question and records it.
func stubConfirmRemove(h *host, answer bool) *string {
	asked := new(string)
	h.confirmRemove = func(q string) (bool, error) { *asked = q; return answer, nil }
	return asked
}

func sp(s string) *string { return &s }
