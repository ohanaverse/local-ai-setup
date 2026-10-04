package agents

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/session"
)

func init() { register("opencode", func() Driver { return opencodeDriver{} }) }

type opencodeDriver struct{}

// YoloFlag is opencode's "--auto": "auto-approve permissions that are not
// explicitly denied". Not an unconditional skip — a permission the user's
// opencode config sets to "deny" stays denied. (opencode 1.18 has no
// "--dangerously-skip-permissions"; passing it is a usage error, exit 1.)
//
// The flag is declared per command, not globally, so opencode's parser only
// knows it is a boolean once the command is resolved: "opencode --auto run
// <prompt>" takes "run" as the flag's VALUE and lands in the default (TUI)
// command. Build therefore emits it leading only for the interactive launch
// (where wt follows it with nothing or another flag), and the one-shot form
// puts it after the subcommand — see OneShotYoloArgs.
func (opencodeDriver) YoloFlag() string { return "--auto" }

func (opencodeDriver) Protocols() []Protocol { return []Protocol{config.ProtocolOpenAIChat} }

func (opencodeDriver) ResumeFlag() string { return "--session" }

// opencodeDBQuery is the seam LatestSession reads opencode's session table
// through. Tests replace it; production runs realOpenCodeDBQuery.
var opencodeDBQuery = realOpenCodeDBQuery

// realOpenCodeDBQuery runs SQL against opencode's database via `opencode db`.
//
// Going through opencode instead of a bare `sqlite3` binary is deliberate.
// opencode resolves its own database location — honoring the OPENCODE_DB
// override and the per-channel `opencode-<channel>.db` filename — and opens it
// with its own bundled SQLite, so wt neither reproduces the path rules nor has
// to get WAL handling right. A missing `sqlite3` CLI, meanwhile, used to make
// every opencode launch fail. `--format json` keeps the rows parseable without
// inventing a separator. opencode creates its data directory when none exists,
// which is the same thing it does on first run; callers only reach this from a
// launch of opencode itself.
func realOpenCodeDBQuery(sql string) ([]byte, error) {
	out, err := exec.Command("opencode", "db", sql, "--format", "json").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

// LatestSession returns the newest top-level, unarchived opencode session
// recorded for exactly path. opencode keeps sessions in SQLite (the session
// table), not the old storage/session/<project-id>/*.json files (#162).
// Keying on the session's own directory column, rather than on opencode's
// project id, matches how claude's lookup is per-worktree-path: a project id
// is shared by every worktree of a repo, so a project-keyed lookup would offer
// another worktree's conversation. An empty result means opencode has no
// session for this path — no session, no error.
func (opencodeDriver) LatestSession(path string) (*session.Session, error) {
	query := "select id, time_updated from session where directory = '" +
		strings.ReplaceAll(path, "'", "''") +
		"' and parent_id is null and time_archived is null" +
		" order by time_updated desc limit 1"
	out, err := opencodeDBQuery(query)
	if err != nil {
		return nil, fmt.Errorf("reading opencode sessions: %w", err)
	}
	var rows []struct {
		ID      string `json:"id"`
		Updated int64  `json:"time_updated"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("reading opencode sessions: unexpected output %q: %w",
			strings.TrimSpace(string(out)), err)
	}
	if len(rows) == 0 || rows[0].ID == "" {
		return nil, nil
	}
	return &session.Session{ID: rows[0].ID, MTime: time.UnixMilli(rows[0].Updated)}, nil
}

// OpenCode routes through a wt-declared custom provider
// (@ai-sdk/openai-compatible, chat completions wire) in both gateway and
// direct mode. The builtin "openai" provider cannot serve registry ids
// (opencode resolves model ids against its own catalog → "Model not found")
// and its models.dev path speaks the responses API, whose bridged stream
// opencode cannot map.
//
// The model ref is "agent-wt/<provider-side-name>". OpenCode splits model
// refs on the first slash, so "agent-wt/<ModelName>" selects the wt provider
// while the provider-side model name survives verbatim inside it. The
// provider's own models map registers the same name so catalog-unknown IDs
// do not raise ProviderModelNotFoundError. small_model is pinned to the
// same provider so background summarization does not query the endpoint with
// names it does not expose (default gpt-5-nano → 400).
func (opencodeDriver) Build(m config.Model, yolo bool, r Route) LaunchCmd {
	lc := LaunchCmd{Bin: "opencode"}
	if yolo {
		lc.Args = append(lc.Args, opencodeDriver{}.YoloFlag())
	}
	// Native dispatch: opencode is ollama-only in normal use, so it never
	// received a Native model before the unconfigured-agent passthrough
	// sentinel (issue #147). Without this guard, a native launch would
	// still emit OPENCODE_CONFIG_CONTENT pointing at an empty gateway base
	// URL instead of a bare `opencode` command.
	if m.Native {
		return lc
	}
	baseURL := r.BaseOrigin + "/v1"
	modelRef := opencodeGatewayProviderID + "/" + r.ModelRef
	lc.Env = append(lc.Env, "OPENCODE_CONFIG_CONTENT="+fmt.Sprintf(
		`{"model":%q,"small_model":%q,"provider":{%q:{"npm":"@ai-sdk/openai-compatible","name":"Agent WT Gateway","options":{"baseURL":%q,"apiKey":%q},"models":{%q:{"name":%q}}}}}`,
		modelRef, modelRef, opencodeGatewayProviderID, baseURL, r.APIKey, r.ModelRef, r.Display,
	))
	return lc
}

// ProfileMechanisms declares opencode accepts env and config_file —
// config_file is merged into the OPENCODE_CONFIG_CONTENT env payload
// (internal/profiles' ApplyConfigContent), not written as a separate
// file.
func (opencodeDriver) ProfileMechanisms() []profiles.Mechanism {
	return []profiles.Mechanism{profiles.MechanismEnv, profiles.MechanismConfigFile}
}

// opencodeGatewayProviderID names the custom provider wt declares in
// OPENCODE_CONFIG_CONTENT for both LiteLLM-routed and direct launches. It
// must not collide with a models.dev-known provider id (those get catalog-
// validated); a unique id + npm @ai-sdk/openai-compatible makes opencode treat
// it as fully custom.
const opencodeGatewayProviderID = "agent-wt"

// OneShotArgs runs a single prompt non-interactively and exits — used by
// wt smoke to verify a model works through this agent.
func (opencodeDriver) OneShotArgs(prompt string) []string { return []string{"run", prompt} }

// OneShotYoloArgs is the one-shot form with permissions auto-approved:
// "run --auto <prompt>", the flag after the subcommand (see YoloFlag for why
// it cannot precede it).
func (opencodeDriver) OneShotYoloArgs(prompt string) []string {
	return []string{"run", opencodeDriver{}.YoloFlag(), prompt}
}
