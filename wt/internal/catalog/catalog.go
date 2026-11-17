// Package catalog owns the model-selector Row shared by wt's pickers and its
// non-TUI launch path: a row's live presence status and what selecting it can
// do (launch, start, or block with a reason). Which candidates become rows,
// from a live inventory snapshot, is the host half and lives in
// internal/localmodels (CatalogRows, MissingReason), so a session package
// that only reads rows never links the probes. It knows nothing about
// rendering, usage counts or sorting — those stay with the callers, which wrap
// a Row in their own type.
package catalog

import (
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// Status is a row's presence state.
type Status string

const (
	StatusOK      Status = "ok"      // on disk (local) or simply available (cloud)
	StatusUnknown Status = "unknown" // local model whose presence the probe could not determine
	StatusNew     Status = "new"     // discovered, unregistered
)

// Action is what selecting a row does.
type Action int

const (
	ActionLaunch Action = iota // cloud row, or a local model that is running and loaded
	ActionStart                // a local model wt can start (its provider has a lifecycle backend)
	ActionBlock                // cannot proceed; BlockReason says why
)

// Row is one model-selector row before rendering.
type Row struct {
	Model    config.Model
	Location config.Location
	Status   Status
	Running  bool
	// Loading: the model is mid-load on omlx (localmodels.Entry.Loading,
	// #259). Running is true too; Ready is what says it can take a request.
	Loading    bool
	Discovered bool
	// Unmapped marks a cloud row whose provider has no LiteLLM mapping: it
	// is in the catalog (every configured model is, #179) but sync never
	// routes it, so a launch through the proxy would fail with "Invalid
	// model name". The model server blocks such a row
	// (internal/modelserver's catalog); a row FromWire converts follows that
	// block.
	Unmapped bool
	// Profile is set on a slot-profile row and nil on every model row. A
	// profile row's Model holds the row id ("profile/<name>") and nothing
	// else, so code that reads another Model field, resolves a route or
	// starts the model must test Profile first.
	Profile *ProfileRow
	// Wire is the server catalog row this Row was made from (FromWire). When
	// set, Ready, Action and BlockReason answer from it, not from the local
	// lifecycle rules, since the server already decided them; Row.Action()
	// follows the wire.
	Wire *modelapi.Row
}

// ModelRow is the row of one model before any live inventory is read: its
// location resolved through cfg (the model's own when cfg is nil or cannot
// resolve it), status ok, not running, and a cloud model whose provider has
// no LiteLLM mapping marked Unmapped (sync never routes it). It is the whole
// row for a caller with no probe to ask, and the starting point
// localmodels.CatalogRows refines from an inventory, so the two cannot
// disagree about a cloud model or a location.
func ModelRow(cfg *config.Config, m config.Model) Row {
	loc := m.Location
	if cfg != nil {
		if l, err := cfg.ResolveLocation(m); err == nil {
			loc = l
		}
	}
	r := Row{Model: m, Location: loc, Status: StatusOK}
	if _, ok := provider.PolicyFor(m.ProviderID); !ok && !m.Native && loc == config.LocationCloud {
		r.Unmapped = true
	}
	return r
}

// ModelRows is ModelRow over models, in order: the rows of a picker that
// reads no inventory (every local row reads as not running, status ok, and
// startable — the fail-open reading localmodels.CatalogRows gives a nil
// Inventory). Discovered rows exist only with an inventory, so none appear.
func ModelRows(cfg *config.Config, models []config.Model) []Row {
	rows := make([]Row, len(models))
	for i, m := range models {
		rows[i] = ModelRow(cfg, m)
	}
	return rows
}

// PairingStartCommand is the command that starts an mlx_lm_server
// target+draft pairing beside the other local models: wt has no engine for
// one, llmbench does. --solo is what keeps the other providers running. An
// empty target or draft (a row with no fetch or draft table) is spelled as a
// placeholder, so the hint still shows the command's shape. A side a shell
// would split or expand — a local path with a space in it — is quoted, so
// the line can be pasted; a repo id and an ordinary path are printed bare.
func PairingStartCommand(target, draft string) string {
	side := func(s, placeholder string) string {
		if s == "" {
			return placeholder
		}
		return shellArg(s)
	}
	return "llmbench provider isolate --solo mlx_lm_server " + side(target, "<target>") + " --draft " + side(draft, "<draft>")
}

// shellArg spells s as one word of a POSIX shell command line. A word made
// only of characters no shell treats specially is returned as it is.
// Anything else is single-quoted, with a leading "~/" left outside the
// quotes so the shell still expands it.
func shellArg(s string) string {
	plain := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r)
	}
	home, rest := "", s
	if strings.HasPrefix(s, "~/") {
		home, rest = "~/", s[2:]
	}
	if s == "~" || !strings.ContainsFunc(rest, func(r rune) bool { return !plain(r) }) {
		return s
	}
	return home + "'" + strings.ReplaceAll(rest, "'", `'\''`) + "'"
}

// NotRunning is the one wording for a local model that is not running and
// that wt cannot start: an mlx_lm_server pairing gets the command that
// starts it; any other provider with no lifecycle engine in wt is named, and
// left to its own tool. Row.BlockReason and localmodels.MissingReason both
// say it, so a blocked row and a pin with no row read the same.
func NotRunning(m config.Model) string {
	if runningOnly(m.ProviderID) {
		return fmt.Sprintf("local model %q is not running, and wt cannot start an mlx_lm_server pairing — start it with `%s`",
			m.ID, PairingStartCommand(m.Fetch.Target(), m.Draft.Target()))
	}
	return fmt.Sprintf("local model %q is not running, and wt cannot start provider %q — start it with that provider's own tool", m.ID, m.ProviderID)
}

// Find returns the row for id, discovered rows included — a -M pin names a
// model, not necessarily a registry entry.
func Find(rows []Row, id string) (Row, bool) {
	for _, r := range rows {
		if r.Model.ID == id {
			return r, true
		}
	}
	return Row{}, false
}

// Ready reports whether the row's model can take a request now: running, and
// not still loading. A cloud row is never Running, so it is never Ready; its
// launch does not depend on this.
func (r Row) Ready() bool {
	if r.Wire != nil {
		return r.Wire.State == modelapi.StateRunning
	}
	return r.Running && !r.Loading
}

// Action reports what selecting r does. A local row that is not Ready is
// startable when its provider has a lifecycle backend (startable: the
// provider table's tenancy, which lifecycle's own Startable is tested to
// equal, so a new backend needs no edit here; a model
// missing from disk has no row at all). That includes a model omlx is still
// loading (#259): starting it joins the load in progress and returns once the
// model is loaded, where launching it would hand an agent a model that cannot
// answer yet.
func (r Row) Action() Action {
	if r.Profile != nil {
		return r.Profile.Verdict
	}
	if r.Wire != nil {
		return r.wireAction()
	}
	if r.Location != config.LocationLocal || r.Ready() {
		return ActionLaunch
	}
	if !startable(r.Model.ProviderID) {
		return ActionBlock
	}
	return ActionStart
}

// startable reports whether wt has a lifecycle backend for providerID's
// family. It reads internal/provider's table (a family with a tenancy has a
// backend), which TestBackendsMatchProviderTable pins to lifecycle.Startable,
// so this package does not link the lifecycle engine.
func startable(providerID string) bool {
	f, ok := provider.FamilyByName(provider.FamilyOf(providerID))
	return ok && f.Tenancy != provider.NoTenancy
}

// runningOnly reports whether providerID's family is probed for running
// state only (mlx_lm_server: wt can neither discover nor start a pairing). It
// reads internal/provider's table, as localmodels.RunningOnly does (a test
// pins the two together), so this package does not link the local-model
// probes.
func runningOnly(providerID string) bool {
	f, ok := provider.FamilyByName(provider.FamilyOf(providerID))
	return ok && f.RunningOnly
}

// BlockReason is the status line for an ActionBlock row ("" otherwise): a
// local model that is not running, on a provider wt has no engine to start.
func (r Row) BlockReason() string {
	if r.Profile != nil {
		return r.Profile.Reason
	}
	if r.Wire != nil {
		return r.Wire.BlockReason
	}
	if r.Action() != ActionBlock {
		return ""
	}
	return NotRunning(r.Model)
}
