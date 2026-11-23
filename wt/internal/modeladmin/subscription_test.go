package modeladmin

import (
	"errors"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func intp(n int) *int { return &n }

// TestAddSubscriptionModelUsesTheBareID pins decision 5's representation: a
// claude-subscription model is registered under the bare real Claude id (no
// provider prefix, no "--", no date suffix), equal to its model_name, which is
// the id the gateway, Claude Code and LiteLLM's anthropic/<id> row all name;
// the provider row is seeded in the same write with no base_url key.
func TestAddSubscriptionModelUsesTheBareID(t *testing.T) {
	path := scratchRegistry(t, baseRegistry)
	res, err := Add(AddRequest{
		ProviderID: "claude-subscription", ModelName: "claude-opus-5-5",
		Fields: Fields{Family: ptr("claude"), ContextWindow: intp(1000000)},
	}, config.SeedEnv{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "claude-opus-5-5" {
		t.Errorf("id = %q, want the bare claude-opus-5-5", res.ID)
	}
	got := read(t, path)
	row := sliceRow(got, "claude-opus-5-5")
	for _, want := range []string{`model_name = "claude-opus-5-5"`, `provider_id = "claude-subscription"`, `context_window = 1000000`, `family = "claude"`} {
		if !strings.Contains(row, want) {
			t.Errorf("row lacks %s:\n%s", want, row)
		}
	}
	if !strings.Contains(got, `id = "claude-subscription"`) || !strings.Contains(got, `type = "client-login"`) {
		t.Errorf("the provider row was not seeded:\n%s", got)
	}
	if i := strings.Index(got, `id = "claude-subscription"`); i >= 0 && strings.Contains(got[i:], "base_url") {
		t.Errorf("the seeded provider row has a base_url:\n%s", got[i:])
	}
	// --id may only restate the bare id.
	for _, id := range []string{"claude-subscription/claude-opus-5-5", "anthropic/claude-opus-5-5", "claude-sonnet-5-5"} {
		_, err := Add(AddRequest{ProviderID: "claude-subscription", ModelName: "claude-opus-5-5", ID: id,
			Fields: Fields{Family: ptr("claude"), ContextWindow: intp(1000000)}}, config.SeedEnv{})
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != FieldID {
			t.Errorf("--id %q: err = %v, want an id refusal", id, err)
		}
	}
	// A name that is not a bare id is refused too: the row would be
	// anthropic/<prefix>/<name>.
	for _, name := range []string{"anthropic/claude-opus-5-5", "claude-opus-5-5[1m]", "claude opus"} {
		if _, err := Add(AddRequest{ProviderID: "claude-subscription", ModelName: name,
			Fields: Fields{Family: ptr("claude"), ContextWindow: intp(1000000)}}, config.SeedEnv{}); err == nil {
			t.Errorf("model name %q was accepted", name)
		}
	}
}

// TestAddSubscriptionModelNeedsContextWindow pins that a subscription model is
// refused without a context window: claudeWindow withdraws the window
// declaration for the whole profile when one slot has none, so a model added
// without it would quietly shrink every session that uses it.
func TestAddSubscriptionModelNeedsContextWindow(t *testing.T) {
	scratchRegistry(t, baseRegistry)
	for _, w := range []*int{nil, intp(0)} {
		_, err := Add(AddRequest{ProviderID: "claude-subscription", ModelName: "claude-haiku-5-5",
			Fields: Fields{Family: ptr("claude"), ContextWindow: w}}, config.SeedEnv{})
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != FieldContextWindow ||
			err.Error() != "a claude-subscription model needs --context-window (Claude Code's window for it)" {
			t.Errorf("window %v: err = %v, want the --context-window refusal", w, err)
		}
	}
}

// TestAddSubscriptionModelWritesNoSource pins that a subscription model gets
// no source key: cloud-sync never covers the provider, so "manual" would be a
// claim about a sync that never runs on it.
func TestAddSubscriptionModelWritesNoSource(t *testing.T) {
	path := scratchRegistry(t, baseRegistry)
	if _, err := Add(AddRequest{ProviderID: "claude-subscription", ModelName: "claude-haiku-5-5",
		Fields: Fields{Family: ptr("claude"), ContextWindow: intp(1000000)}}, config.SeedEnv{}); err != nil {
		t.Fatal(err)
	}
	if row := sliceRow(read(t, path), "claude-haiku-5-5"); strings.Contains(row, "source") {
		t.Errorf("subscription row has a source key:\n%s", row)
	}
}

// TestBareIDRefusedForOtherProviders pins that the bare-id exception belongs
// to the subscription provider alone: every other provider's id stays
// <provider>/<name>, which the rest of wt takes for granted (`wt stop <arg>`
// reads a slash-less argument as a provider), and DeriveID keeps its rule.
func TestBareIDRefusedForOtherProviders(t *testing.T) {
	scratchRegistry(t, baseRegistry)
	_, err := Add(AddRequest{ProviderID: "openrouter", ModelName: "claude-haiku-5-5", ID: "claude-haiku-5-5",
		Fields: Fields{Family: ptr("claude")}}, config.SeedEnv{})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != FieldID {
		t.Errorf("openrouter with a bare id: err = %v, want an id refusal", err)
	}
	if got := DeriveID("openrouter", "claude-haiku-5-5"); got != "openrouter/claude-haiku-5-5" {
		t.Errorf("DeriveID(openrouter) = %q", got)
	}
	if got := DeriveID("claude-subscription", "claude-haiku-5-5"); got != "claude-haiku-5-5" {
		t.Errorf("DeriveID(claude-subscription) = %q, want the bare id", got)
	}
}

// TestEditSubscriptionModelKeepsItsWindow pins that `wt-model model edit
// <subscription model> --context-window 0` is refused with the add's
// message and leaves the row alone: clearing the window would withdraw the
// window declaration of every profile the model is in, exactly what the add
// refuses. Another field still edits, and another provider may clear it.
func TestEditSubscriptionModelKeepsItsWindow(t *testing.T) {
	path := scratchRegistry(t, baseRegistry)
	if _, err := Add(AddRequest{ProviderID: "claude-subscription", ModelName: "claude-haiku-5-5",
		Fields: Fields{Family: ptr("claude"), ContextWindow: intp(1000000)}}, config.SeedEnv{}); err != nil {
		t.Fatal(err)
	}
	before := read(t, path)
	_, err := Edit("claude-haiku-5-5", Fields{ContextWindow: intp(0)})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != FieldContextWindow || err.Error() != subscriptionWindowRefusal {
		t.Fatalf("edit --context-window 0: err = %v, want the add's refusal", err)
	}
	if read(t, path) != before {
		t.Fatal("a refused edit changed the registry")
	}
	if _, err := Edit("claude-haiku-5-5", Fields{Tags: ptr("code")}); err != nil {
		t.Errorf("an unrelated edit was refused: %v", err)
	}
	if _, err := Edit("ollama/gemma4:9b", Fields{ContextWindow: intp(0)}); err != nil {
		t.Errorf("clearing another provider's window was refused: %v", err)
	}
}
