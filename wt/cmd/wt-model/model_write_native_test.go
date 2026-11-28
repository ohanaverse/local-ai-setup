package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
)

// TestAddDoesNotReseedARemovedNativeRow pins that an add seeds a native row
// only for the model that names it. The registry is that of a user who
// deleted the codex block by hand (no command removes a provider row): adding
// an openrouter model prints no "added provider: codex" and leaves the
// registry without a codex block, whereas the next `model init` adds exactly
// codex back. Before, an add re-seeded every native row of the agents named
// in config.toml, so a hand deletion lasted only until the next add.
func TestAddDoesNotReseedARemovedNativeRow(t *testing.T) {
	native := func(id, name string) string {
		return "\n[[providers]]\nid = \"" + id + "\"\nname = \"" + name + "\"\nlocation = \"cloud\"\n\n[providers.auth]\ntype = \"native\"\n"
	}
	h := testHost()
	registry := modelHome(t, writeRegistry+native("claude", "Claude")+native("copilot", "Copilot")+native("agy", "Agy"))
	stubSeedEnv(h, config.SeedEnv{})
	stubRouteSync(h, "")

	var out, errOut bytes.Buffer
	req := modeladmin.AddRequest{ProviderID: "openrouter", ModelName: "qwen/qwen3.8-27b", Fields: modeladmin.Fields{Family: sp("qwen3.8")}}
	if err := h.runModelAdd(&out, &errOut, nil, req); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "added provider") {
		t.Errorf("the add seeded a provider row: %q", out.String())
	}
	if got := mustRead(t, registry); strings.Contains(got, `id = "codex"`) {
		t.Errorf("the add re-created the removed codex row:\n%s", got)
	}

	out.Reset()
	if err := h.runModelInit(&out, &errOut, false); err != nil {
		t.Fatal(err)
	}
	if want := "registry: " + registry + "\nadded provider: codex\n"; out.String() != want {
		t.Errorf("init stdout = %q, want %q", out.String(), want)
	}
}
