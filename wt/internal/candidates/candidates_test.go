package candidates

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

func f(v float64) *float64 { return &v }

func model(provider, name string, out *float64) config.Model {
	return config.Model{
		ID: provider + "/" + name, Family: "x", ProviderID: provider, ModelName: name,
		Location: config.LocationCloud, Cost: config.ModelCost{OutputPricePerMillion: out},
	}
}

func entry(provider, name, tier, code, conf string, score float64, extra string) string {
	s := "[[models]]\nprovider_id = \"" + provider + "\"\nmodel_name = \"" + name + "\"\ntier = \"" + tier +
		"\"\ncode = \"" + code + "\"\nconfidence = \"" + conf + "\"\n"
	if score >= 0 {
		s += "aligned_score = " + strconv.FormatFloat(score, 'f', -1, 64) + "\n"
	}
	return s + extra + "\n"
}

func mustParse(t *testing.T, doc string) tiers.Index {
	t.Helper()
	ix, err := tiers.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func ids(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

// TestRankOrdersOpenThenScoreThenPrice pins Rank's whole ordering — open
// weights first, then score descending (no score last), then output price
// ascending (no price last), then id — because the skill reads the top of
// this list as its proposals.
func TestRankOrdersOpenThenScoreThenPrice(t *testing.T) {
	ix := mustParse(t, entry("openrouter", "closed-hi", "deep", "D", "high", 60, "open_weights = false\ntool_call = true")+
		entry("openrouter", "open-lo", "quick", "Q", "high", 30, "open_weights = true\ntool_call = true")+
		entry("openrouter", "open-hi-dear", "standard", "S", "high", 50, "open_weights = true\ntool_call = true")+
		entry("openrouter", "open-hi-cheap", "standard", "S", "high", 50, "open_weights = true\ntool_call = true")+
		entry("openrouter", "open-hi-noprice", "standard", "S", "high", 50, "open_weights = true\ntool_call = true")+
		entry("openrouter", "open-none", "unrated", "-", "none", -1, "open_weights = true\ntool_call = true"))
	models := []config.Model{
		model("openrouter", "closed-hi", f(1)), model("openrouter", "open-lo", f(1)),
		model("openrouter", "open-hi-dear", f(5)), model("openrouter", "open-hi-cheap", f(2)),
		model("openrouter", "open-hi-noprice", nil), model("openrouter", "open-none", f(1)),
	}
	got := ids(Rank(models, ix, Query{}))
	want := []string{"openrouter/open-hi-cheap", "openrouter/open-hi-dear", "openrouter/open-hi-noprice",
		"openrouter/open-lo", "openrouter/open-none", "openrouter/closed-hi"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v\nwant    %v", got, want)
	}
	if got := ids(Rank(models, ix, Query{OpenOnly: true})); len(got) != 5 {
		t.Errorf("OpenOnly = %v", got)
	}
}

// TestRankExcludesToolCallFalse checks that a model tiers.toml says cannot
// call tools is left out entirely: an agent slot needs tool calling, so
// proposing it would waste the user's time.
func TestRankExcludesToolCallFalse(t *testing.T) {
	ix := mustParse(t, entry("p", "no", "deep", "D", "high", 50, "tool_call = false")+
		entry("p", "yes", "deep", "D", "high", 50, "tool_call = true"))
	got := ids(Rank([]config.Model{model("p", "no", nil), model("p", "yes", nil)}, ix, Query{}))
	if !reflect.DeepEqual(got, []string{"p/yes"}) {
		t.Errorf("got %v", got)
	}
}

// TestRankFlagsUnknownToolCall checks that a model with no tool_call key is
// listed with a flag rather than hidden: "unknown" must stay visible so the
// skill can name it and offer research or a smoke test.
func TestRankFlagsUnknownToolCall(t *testing.T) {
	ix := mustParse(t, entry("p", "m", "deep", "D", "low", 50, ""))
	got := Rank([]config.Model{model("p", "m", nil)}, ix, Query{})
	want := []string{"low-confidence", "tool_call unknown"}
	if len(got) != 1 || !reflect.DeepEqual(got[0].Flags, want) {
		t.Errorf("got %+v", got)
	}
}

// TestRankMissingTiersLeavesAllUnratedWithFlag checks the no-tiers.toml
// path: every real model is still listed, flagged unrated, with profile/
// and claude/ pseudo-ids and unrated embeddings skipped — a missing file
// degrades the ranking, never the command.
func TestRankMissingTiersLeavesAllUnratedWithFlag(t *testing.T) {
	models := []config.Model{model("p", "b", nil), model("p", "a", nil),
		{ID: "profile/x"}, {ID: "claude/y"}, {ID: "p/text-embed-1", ProviderID: "p", ModelName: "text-embed-1"}}
	got := Rank(models, tiers.Index(nil), Query{})
	if !reflect.DeepEqual(ids(got), []string{"p/a", "p/b"}) {
		t.Fatalf("got %v", ids(got))
	}
	for _, c := range got {
		if c.Rated || c.HasScore || c.Flags[0] != "unrated" {
			t.Errorf("%+v", c)
		}
	}
}

// TestRankSubscriptionFlag checks that a subscription-priced model keeps
// both its per-token price and its subscription flag, so the skill can say
// the two are not comparable instead of comparing them.
func TestRankSubscriptionFlag(t *testing.T) {
	m := model("p", "sub", f(3))
	m.Cost.SubscriptionPrice = f(20)
	m.Cost.SubscriptionPeriod = "month"
	ix := mustParse(t, entry("p", "sub", "deep", "D", "high", 50, "tool_call = true"))
	got := Rank([]config.Model{m}, ix, Query{})
	if len(got) != 1 || !reflect.DeepEqual(got[0].Flags, []string{"subscription"}) ||
		got[0].Subscription == nil || *got[0].Subscription != 20 || got[0].SubscriptionPeriod != "month" ||
		got[0].OutputPerMillion == nil || *got[0].OutputPerMillion != 3 {
		t.Errorf("got %+v", got)
	}
}

// TestRankProvidersFilter checks that --provider narrows the ranking by
// provider id, which is how the skill scopes a twin pair to two providers.
func TestRankProvidersFilter(t *testing.T) {
	ix := mustParse(t, entry("a", "m", "deep", "D", "high", 50, "tool_call = true")+
		entry("b", "m", "deep", "D", "high", 50, "tool_call = true"))
	got := ids(Rank([]config.Model{model("a", "m", nil), model("b", "m", nil)}, ix, Query{Providers: []string{"b"}}))
	if !reflect.DeepEqual(got, []string{"b/m"}) {
		t.Errorf("got %v", got)
	}
}

// TestRankNearPerProvider checks --near: the rated models nearest in score
// to the reference, per provider (nearest first, capped at PerProvider),
// with the reference itself, unscored and unrated models left out — and an
// unknown or unscored reference yields an empty list, not an error.
func TestRankNearPerProvider(t *testing.T) {
	doc := entry("ollama", "near", "standard", "S", "high", 44.8, "tool_call = true")
	var models []config.Model
	models = append(models, model("ollama", "near", nil))
	scores := map[string]float64{"a": 40, "b": 45, "c": 50, "d": 44, "e": 60}
	for _, p := range []string{"ollama", "openrouter"} {
		for n, s := range scores {
			doc += entry(p, n, "standard", "S", "high", s, "tool_call = true")
			models = append(models, model(p, n, nil))
		}
	}
	// unscored model must never appear
	models = append(models, model("ollama", "zz", nil))
	got := ids(Rank(models, mustParse(t, doc), Query{Near: "ollama/near"}))
	want := []string{"ollama/b", "ollama/d", "ollama/a", "openrouter/b", "openrouter/d", "openrouter/a"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if got := Rank(models, mustParse(t, doc), Query{Near: "ollama/zz"}); len(got) != 0 {
		t.Errorf("unscored near: %v", ids(got))
	}
	if got := Rank(models, mustParse(t, doc), Query{Near: "nope/x"}); len(got) != 0 {
		t.Errorf("unknown near: %v", ids(got))
	}
}
