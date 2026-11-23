package main

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestModelAddSubscriptionThroughTheCommandLine drives `wt-model model add
// claude-subscription <id> --family <f>` as a user types it: without
// --context-window it is refused with decision 5's wording and the registry
// is left alone (a subscription model without a window withdraws the window
// declaration of every profile it is in); with it, the model is written under
// the bare id and its provider row is seeded in the same write.
func TestModelAddSubscriptionThroughTheCommandLine(t *testing.T) {
	h := testHost()
	registry := modelHome(t, writeRegistry)
	stubSeedEnv(h, config.SeedEnv{})
	syncs := stubRouteSync(h, "")
	before := mustRead(t, registry)
	_, err := runWTModelOn(t, h, "model", "add", "claude-subscription", "claude-haiku-5-5", "--family", "claude")
	if err == nil || !strings.Contains(err.Error(), "a claude-subscription model needs --context-window (Claude Code's window for it)") {
		t.Fatalf("add without --context-window: err = %v", err)
	}
	if mustRead(t, registry) != before || *syncs != 0 {
		t.Fatalf("a refused add changed the registry or synced (%d)", *syncs)
	}
	out, err := runWTModelOn(t, h, "model", "add", "claude-subscription", "claude-haiku-5-5", "--family", "claude", "--context-window", "1000000")
	if err != nil || !strings.HasPrefix(out, "added model: claude-haiku-5-5\n") {
		t.Fatalf("add = %q, %v", out, err)
	}
	got := mustRead(t, registry)
	if !strings.Contains(got, "id = \"claude-haiku-5-5\"\nfamily = \"claude\"\nprovider_id = \"claude-subscription\"\nmodel_name = \"claude-haiku-5-5\"") ||
		!strings.Contains(got, "context_window = 1000000") || !strings.Contains(got, "[[providers]]\nid = \"claude-subscription\"") {
		t.Errorf("registry =\n%s", got)
	}
}
