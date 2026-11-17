// Tests for what a model-selector row does: its action, the reason a blocked
// row gives, and the lookup by id. Which models become rows is the host half
// (localmodels.CatalogRows) and is tested there.
package catalog

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestRowActionRules verifies what selecting a row does per row kind: cloud
// and running local rows launch; a non-running local row of a provider wt can
// start (ollama, omlx, mtplx) starts — including a pulled ollama model, a
// discovered row and an unknown-presence row; a provider with no start
// engine (mlx_lm_server) is blocked. Getting this wrong either
// launches a dead model or hides one wt could have started.
func TestRowActionRules(t *testing.T) {
	local := func(provider string, status Status, running, discovered bool) Row {
		return Row{Location: config.LocationLocal, Status: status, Running: running, Discovered: discovered, Model: config.Model{ID: provider + "/x", ProviderID: provider}}
	}
	cases := []struct {
		name string
		row  Row
		want Action
	}{
		{"cloud", Row{Location: config.LocationCloud}, ActionLaunch},
		{"running omlx", local("omlx", StatusOK, true, false), ActionLaunch},
		{"idle omlx on disk", local("omlx", StatusOK, false, false), ActionStart},
		{"idle mtplx discovered", local("mtplx", StatusNew, false, true), ActionStart},
		{"idle ollama pulled", local("ollama", StatusOK, false, false), ActionStart},
		{"idle ollama unknown presence", local("ollama", StatusUnknown, false, false), ActionStart},
		{"mlx_lm_server has no start engine", local("mlx_lm_server", StatusOK, false, false), ActionBlock},
		{"omlx-6bit shares omlx's engine", local("omlx-6bit", StatusOK, false, false), ActionStart},
	}
	for _, tc := range cases {
		if got := tc.row.Action(); got != tc.want {
			t.Errorf("%s: action = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestBlockReasonNamesTheFix verifies the status text for a blocked row names
// what to do: for an mlx_lm_server pairing, the llmbench command that starts
// it beside the other models, with the row's own target and draft; for any
// other provider wt has no engine for, that provider's own tool. It is ""
// for rows that are not blocked.
func TestBlockReasonNamesTheFix(t *testing.T) {
	mlx := Row{Location: config.LocationLocal, Status: StatusOK, Model: config.Model{
		ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server",
		Fetch: config.ModelArtifact{Repo: "org/target"}, Draft: config.ModelArtifact{LocalPath: "/models/draft"},
	}}
	want := "local model \"mlx_lm_server/p\" is not running, and wt cannot start an mlx_lm_server pairing — start it with `llmbench provider isolate --solo mlx_lm_server org/target --draft /models/draft`"
	if h := mlx.BlockReason(); h != want {
		t.Errorf("mlx_lm_server reason = %q\nwant %q", h, want)
	}
	other := Row{Location: config.LocationLocal, Status: StatusOK, Model: config.Model{ID: "llamacpp/old", ProviderID: "llamacpp"}}
	if h := other.BlockReason(); h != `local model "llamacpp/old" is not running, and wt cannot start provider "llamacpp" — start it with that provider's own tool` {
		t.Errorf("reason for a provider with no engine = %q", h)
	}
	if h := (Row{Location: config.LocationCloud}).BlockReason(); h != "" {
		t.Errorf("cloud reason = %q, want empty", h)
	}
	if h := (Row{Location: config.LocationLocal, Status: StatusOK, Model: config.Model{ProviderID: "omlx"}}).BlockReason(); h != "" {
		t.Errorf("startable row reason = %q, want empty", h)
	}
}

// TestPairingStartCommand pins the command the hints print: --solo, so the
// other local providers keep running (llmbench stops them without it), the
// target as the positional and the draft behind --draft, and a placeholder
// for a side the registry row does not record. A side a shell would split or
// expand is quoted (with "~/" left for the shell), because the line is there
// to be pasted: unquoted, `~/My Models/Big-4bit` starts the server on a
// target called ~/My and fails on the rest.
func TestPairingStartCommand(t *testing.T) {
	const prefix = "llmbench provider isolate --solo mlx_lm_server "
	for _, c := range []struct{ target, draft, want string }{
		{"mlx-community/Qwen3.8-27B-4bit", "~/models/Qwen3.8-4B-4bit/", "mlx-community/Qwen3.8-27B-4bit --draft ~/models/Qwen3.8-4B-4bit/"},
		{"~/My Models/Big-4bit", "/quant/it's here/D", `~/'My Models/Big-4bit' --draft '/quant/it'\''s here/D'`},
		{"./out/a b", "org/d;rm", "'./out/a b' --draft 'org/d;rm'"},
		{"~", "~/", "~ --draft ~/"},
	} {
		if got := PairingStartCommand(c.target, c.draft); got != prefix+c.want {
			t.Errorf("PairingStartCommand(%q, %q) =\n%s\nwant\n%s", c.target, c.draft, got, prefix+c.want)
		}
	}
	if got, want := PairingStartCommand("org/t", "org/d"), "llmbench provider isolate --solo mlx_lm_server org/t --draft org/d"; got != want {
		t.Errorf("PairingStartCommand = %q, want %q", got, want)
	}
	if got, want := PairingStartCommand("", ""), "llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>"; got != want {
		t.Errorf("with nothing recorded = %q, want %q", got, want)
	}
}

// TestFindReportsDiscoveredRows verifies Find looks a row up among ALL rows,
// discovered ones included — the fix that lets `wt -M <discovered-id>` work.
// A registry-only lookup would send users to a registry edit they do not need.
func TestFindReportsDiscoveredRows(t *testing.T) {
	rows := []Row{
		{Model: config.Model{ID: "omlx/a", ProviderID: "omlx"}, Location: config.LocationLocal, Status: StatusOK},
		{Model: config.Model{ID: "omlx/disc", ProviderID: "omlx"}, Location: config.LocationLocal, Status: StatusNew, Discovered: true},
	}
	if r, ok := Find(rows, "omlx/disc"); !ok || !r.Discovered {
		t.Errorf("Find(omlx/disc) = (%+v, %v), want the discovered row", r, ok)
	}
	if r, ok := Find(rows, "omlx/a"); !ok || r.Discovered {
		t.Errorf("Find(omlx/a) = (%+v, %v), want the registered row", r, ok)
	}
	if _, ok := Find(rows, "omlx/nope"); ok {
		t.Error("Find(omlx/nope) reported a hit, want miss")
	}
}
