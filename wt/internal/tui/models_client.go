package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// ModelClient is what the picker needs from the model server: the catalog, one
// model's row, a start job, the client session and the gateway the agent is
// launched against. cmd/wt hands in an adapter over *modelclient.Client
// (NewModelClient); tests hand in a fake. A nil ModelClient is the legacy
// registry path, deleted once every caller is on the server.
type ModelClient interface {
	Catalog(ctx context.Context) (modelapi.Catalog, error)
	Resolve(ctx context.Context, id string) (modelapi.Row, error)
	RunStart(ctx context.Context, req modelapi.StartRequest, ans modelclient.Answerer, prog modelclient.Progress) (modelapi.JobStatus, error)
	CancelJob(ctx context.Context, id string) error
	NewSession(models []string, o modelclient.SessionOptions) ClientSession
	Gateway() agents.Gateway
	Now() time.Time
}

// ClientSession is a client session as the picker holds it: what
// *modelclient.Session does (register, change the set, end).
type ClientSession interface {
	Start(ctx context.Context) error
	Add(ctx context.Context, ids ...string) error
	Remove(ctx context.Context, id string) error
	End(ctx context.Context) error
}

// clientAdapter is ModelClient over the real client and its endpoint.
type clientAdapter struct {
	*modelclient.Client
	ep modelclient.Endpoint
}

// NewModelClient adapts c, built for ep, to ModelClient.
func NewModelClient(c *modelclient.Client, ep modelclient.Endpoint) ModelClient {
	return clientAdapter{Client: c, ep: ep}
}

func (a clientAdapter) NewSession(models []string, o modelclient.SessionOptions) ClientSession {
	return a.Client.NewSession(models, o)
}

func (a clientAdapter) Gateway() agents.Gateway {
	return agents.Gateway{Origin: a.ep.Origin(), Token: a.ep.Token}
}

// catalogLoadedMsg is a finished fetch: the table already built (the
// usage and rotation reads happen in the command, never in Update), or the
// reason the picker goes back to the agent screen.
type catalogLoadedMsg struct {
	gen      int
	wire     map[string]modelapi.Row // the eligible rows the table shows, by id
	eligible int                     // rows the agent can use; 0 sends the picker back
	pinErr   string                  // a -M pin the server or the agent cannot use
	tbl      modelTable              // the sorted, rendered table
	cursor   string                  // the id the cursor lands on; "" is the first actionable row
	// pricing is the catalog's pricing member (nil when no model is priced
	// from OpenRouter): the post-launch notice reads it instead of fetching
	// the catalog again.
	pricing *modelapi.Pricing
}

// catalogFailedMsg is a fetch that failed, whatever the reason: an unreachable
// server a retry may heal, but also a refused token, an API skew or a schema
// the client does not know, which F cannot heal (the status line carries the
// error, which says what to fix).
type catalogFailedMsg struct {
	gen int
	err error
}

// catalogRequest is everything the fetch command reads, copied out of the
// model so the goroutine never touches it.
type catalogRequest struct {
	ctx     context.Context // cancelled on esc, a superseding F and the program's end
	client  ModelClient
	gen     int
	agent   config.Agent
	tags    string
	family  string
	pinned  string
	appName string
	// slots are the statically validated slot profiles; the agent's usable
	// ones become profile rows resolved against the catalog.
	slots []profiles.SlotCheck
}

// fetchCatalogCmd asks the server for the catalog (and, for a pin, the pinned
// model's row) and builds the table: all in the command, off the update
// goroutine (T12).
func fetchCatalogCmd(r catalogRequest) tea.Cmd {
	return func() tea.Msg {
		ctx := r.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		cat, err := r.client.Catalog(ctx)
		if err != nil {
			return catalogFailedMsg{gen: r.gen, err: err}
		}
		eligible := catalog.EligibleWire(cat.Rows, r.agent, r.tags, r.family)
		msg := catalogLoadedMsg{gen: r.gen, eligible: len(eligible), pricing: cat.Pricing}
		if len(eligible) == 0 {
			return msg
		}
		profileRows := wireProfileRows(cat.Rows, r.slots, r.agent.Name, r.tags, r.family)
		pinnedProfile := r.pinned != "" && slices.ContainsFunc(profileRows, func(pr catalog.Row) bool { return pr.Model.ID == r.pinned })
		if r.pinned != "" && !pinnedProfile && strings.HasPrefix(r.pinned, profiles.SlotIDPrefix) {
			// A profile the file holds says why it has no row: hidden by -T/-F,
			// or another agent's (the registry path's reason, #412).
			if reason := catalog.SlotPinReason(r.slots, r.agent.Name, r.pinned, r.tags, r.family); reason != "" {
				msg.pinErr = reason
				return msg
			}
			// The pin names a profile the file holds for this agent but the
			// catalog holds the prefix, so no profile row exists: refuse as
			// the CLI's pin does (slotPinRemote then launchSlotProfileRemote)
			// instead of sending it to resolve as a model id.
			if holder := catalog.SlotPrefixHolderWire(cat.Rows); holder != "" && hasUsableSlotProfile(r.slots, r.agent.Name, r.pinned) {
				msg.pinErr = cleanup.Flatten(catalog.SlotPrefixHeldReason(holder))
				return msg
			}
		}
		if r.pinned != "" && !pinnedProfile {
			row, err := r.client.Resolve(ctx, r.pinned)
			var unknown *modelclient.UnknownModelError
			switch {
			case errors.As(err, &unknown):
				msg.pinErr = cleanup.Flatten(err.Error())
				return msg
			case err != nil:
				return catalogFailedMsg{gen: r.gen, err: err}
			}
			i := slices.IndexFunc(eligible, func(e modelapi.Row) bool { return e.ID == r.pinned })
			switch {
			case i < 0:
				msg.pinErr = fmt.Sprintf("model %q is not in the eligible list for agent %q", r.pinned, r.agent.Name)
				return msg
			case row.Action == modelapi.ActionBlock:
				msg.pinErr = cleanup.Flatten(row.BlockReason)
				if msg.pinErr == "" {
					msg.pinErr = fmt.Sprintf("%s cannot be used", r.pinned)
				}
				return msg
			}
			eligible[i] = row // the resolve is the fresher answer
		}
		at, perr := time.Parse(time.RFC3339, cat.Generated)
		if perr != nil {
			at = r.client.Now()
		}
		msg.wire = make(map[string]modelapi.Row, len(eligible))
		for _, e := range eligible {
			msg.wire[e.ID] = cleanWireRow(e)
		}
		rows := wireTableRows(eligible, at)
		for _, pr := range profileRows {
			rows = append(rows, tableRow{Row: pr, at: at, slotTiers: wireSlotTiers(pr, cat.Rows)})
		}
		if store := newUsageStore(); store != nil {
			ids := make([]string, len(rows))
			for i, tr := range rows {
				ids[i] = tr.Model.ID
			}
			counts := store.CountsForAgent(r.agent.Name, ids)
			for i := range rows {
				rows[i].counts = counts[rows[i].Model.ID]
			}
		}
		sortRows(rows)
		refs := make(map[string]int, len(rows))
		for _, tr := range rows {
			if tr.Wire != nil {
				refs[tr.Model.ID] = tr.Wire.Sessions
			}
		}
		rot := rotation.New()
		lastID, _ := rot.LastPicked()
		msg.tbl = renderTable(rows, r.agent.Name, refs, lastID)
		if r.pinned != "" {
			msg.cursor = r.pinned
			return msg
		}
		// The rotation is the non-TUI endpoint launch's: the launchable
		// registered rows in the server's order (resolveModelRemote's list).
		// A row the server would have to start is never the rotation's next.
		var launchable []modelapi.Row
		for _, r := range catalog.Launchable(eligible) {
			if r.Registered {
				launchable = append(launchable, r)
			}
		}
		if next, ok := rot.NextFromOrder(catalog.RegisteredOrdered(launchable), catalog.RegisteredOrdered(cat.Rows)); ok {
			msg.cursor = next.ID
		}
		return msg
	}
}

// hasUsableSlotProfile reports whether checks hold a Usable profile of agent
// named by pin ("profile/<name>").
func hasUsableSlotProfile(checks []profiles.SlotCheck, agent, pin string) bool {
	name, ok := strings.CutPrefix(pin, profiles.SlotIDPrefix)
	return ok && slices.ContainsFunc(checks, func(c profiles.SlotCheck) bool {
		return c.Usable && c.Profile.Agent == agent && c.Profile.Name == name
	})
}

// wireProfileRows is the API table's slot-profile rows: one per usable profile
// of agent, in file order, resolved against the server's catalog
// (catalog.ResolveSlotProfileWire). As on the registry path (catalog's
// slotRows) there are none under a -T/-F filter or while the catalog holds
// the "profile/" prefix, and a group of profiles sharing a name gets one row,
// blocked by the duplicate error every member carries.
func wireProfileRows(rows []modelapi.Row, checks []profiles.SlotCheck, agent, tags, family string) []catalog.Row {
	if len(checks) == 0 || tags != "" || family != "" || catalog.SlotPrefixHolderWire(rows) != "" {
		return nil
	}
	byID := catalog.WireByID(rows)
	var out []catalog.Row
	dup := map[string]bool{}
	for _, c := range checks {
		if !c.Usable || c.Profile.Agent != agent {
			continue
		}
		if c.Duplicate {
			if dup[c.Profile.Name] {
				continue
			}
			dup[c.Profile.Name] = true
		}
		out = append(out, catalog.ResolveSlotProfileWire(byID, c))
	}
	return out
}

// wireSlotTiers is a profile row's slot line tiers from the server's rows:
// each slot model's short tier, for the rated ones only (tableRow.rate's
// rule).
func wireSlotTiers(pr catalog.Row, rows []modelapi.Row) map[string]string {
	byID := catalog.WireByID(rows)
	var out map[string]string
	for slot, m := range pr.Profile.Models {
		w, ok := byID[m.ID]
		if !ok {
			continue
		}
		e := tiers.Entry{Tier: w.Tier, Code: w.TierCode, Confidence: w.Confidence, Sources: w.TierSources}
		if w.Aligned != nil {
			e.Aligned, e.HasAligned = *w.Aligned, true
		}
		if e.Rated() {
			if out == nil {
				out = map[string]string{}
			}
			out[slot] = cleanup.Flatten(e.Short())
		}
	}
	return out
}

// cleanWireRow is r with every string it shows made one printable line (M11).
// modelclient already cleans what it decodes; this is the second wall, for a
// row that reached the table another way.
func cleanWireRow(r modelapi.Row) modelapi.Row {
	for _, p := range []*string{&r.ID, &r.GatewayID, &r.Family, &r.ProviderID, &r.ModelName, &r.Location,
		&r.State, &r.Action, &r.BlockReason, &r.Tier, &r.TierCode, &r.Confidence} {
		*p = cleanup.Flatten(*p)
	}
	r.Tags = cleanAllStrings(r.Tags)
	r.Protocols = cleanAllStrings(r.Protocols)
	return r
}

func cleanAllStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = cleanup.Flatten(s)
	}
	return out
}

// wireTableRows builds the table's rows from the server's catalog rows: the
// price the server stated, priced at at (the catalog's own instant, which is
// what time-priced rows were resolved at), and the tier cell from tier_code
// and aligned. A zero at is a row with no clock. Usage counts stay local and
// are set by the caller.
func wireTableRows(rows []modelapi.Row, at time.Time) []tableRow {
	out := make([]tableRow, len(rows))
	for i, w := range rows {
		r := catalog.FromWire(cleanWireRow(w))
		tr := tableRow{Row: r, at: at}
		if p := r.Wire.Price; p != nil {
			tr.in = config.PriceInForce{Input: p.Input, Cache: p.Cache, Output: p.Output, Row: -1}
			tr.timed = p.TimePriced
		}
		e := tiers.Entry{Tier: r.Wire.Tier, Code: r.Wire.TierCode, Confidence: r.Wire.Confidence, Sources: r.Wire.TierSources}
		if r.Wire.Aligned != nil {
			e.Aligned, e.HasAligned = *r.Wire.Aligned, true
		}
		tr.tier = e
		out[i] = tr
	}
	return out
}

// beginCatalogLoad is the model phase on the API path: a loading screen and a
// command that fetches the catalog. Nothing here waits.
func (m model) beginCatalogLoad() (model, tea.Cmd) {
	ag, err := m.cfg.AgentByName(m.agent)
	if err != nil {
		m.status = "config error: " + err.Error()
		return m, nil
	}
	m.tag = config.FirstTag(m.activeTags, m.cfg.DefaultTag)
	return m.fetchCatalog(*ag)
}

// fetchCatalog starts (or restarts) the fetch behind the loading screen.
func (m model) fetchCatalog(ag config.Agent) (model, tea.Cmd) {
	m.refreshGen++
	m.phase = phaseCatalog
	m.status = ""
	m.catalogFailed = false
	if m.catalogCancel != nil {
		m.catalogCancel() // a fetch this one supersedes
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.catalogCancel = cancel
	return m, fetchCatalogCmd(catalogRequest{
		ctx: ctx, client: m.client, gen: m.refreshGen, agent: ag, tags: m.activeTags, family: m.activeFamily, pinned: m.pinnedModel,
		slots: m.slotChecks,
	})
}

// handleCatalogMsg applies a finished or failed fetch. A message for an
// earlier fetch, or one that lands after the user left, is dropped.
func (m model) handleCatalogMsg(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case catalogFailedMsg:
		if m.phase != phaseCatalog || msg.gen != m.refreshGen {
			return m, nil
		}
		m.catalogFailed = true
		m.status = "error: " + cleanup.Flatten(msg.err.Error())
	case catalogLoadedMsg:
		if m.phase != phaseCatalog || msg.gen != m.refreshGen {
			return m, nil
		}
		m.catPricing, m.catSeen = msg.pricing, true
		switch {
		case msg.pinErr != "":
			return m.routeBackToAgent(msg.pinErr)
		case msg.eligible == 0:
			return m.routeBackToAgent(fmt.Sprintf("no models for agent %q in tag %q on the model server", m.agent, m.tag))
		}
		m.models = m.newModelList(msg.tbl, msg.cursor)
		m.wire = msg.wire
		m.status = ""
		m.catalogFailed = false
		m.phase = phaseModel
		m.fitLists()
		if m.pinnedModel != "" {
			// The pin is the user's act, as on the registry path: a row the
			// server must start starts (with --replace and --others, the
			// pin's permissions), any other launches at once.
			if it, ok := m.models.SelectedItem().(*modelItem); ok && it.model.ID == m.pinnedModel {
				return m.chooseRemote(it, true)
			}
		}
	}
	return m, nil
}

// newModelList is the picker's list for tbl with the cursor on cursor, else
// on the first row that is not blocked.
func (m model) newModelList(tbl modelTable, cursor string) list.Model {
	delegate := ThemedListDelegate(m.theme)
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	items := make([]list.Item, len(tbl.items))
	pos, first := -1, -1
	for i, it := range tbl.items {
		items[i] = it
		if pos < 0 && cursor != "" && it.model.ID == cursor {
			pos = i
		}
		if first < 0 && it.blocked == "" {
			first = i
		}
	}
	ml := list.New(items, delegate, m.width-2, m.height-2)
	ml.Title = tbl.header
	styleTableTitle(&ml, m.theme)
	// As every picker list: no status bar (its "N items" line is room the
	// fit does not count) and a cursor that wraps at either edge (spec §6).
	ml.SetShowStatusBar(false)
	ml.InfiniteScrolling = true
	if pos < 0 {
		pos = max(first, 0)
	}
	ml.Select(pos)
	return ml
}

// handleCatalogKey is the keyboard while the catalog loads or its fetch
// failed. ctrl+c and q leave; esc goes back to the agent screen (or leaves
// when the agent was pinned and there is none); F fetches again, offered and
// acting only while a failure shows.
func (m model) handleCatalogKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc":
		if m.initialAgent != "" {
			return m, tea.Quit
		}
		m.refreshGen++ // a fetch still in flight lands on nothing
		if m.catalogCancel != nil {
			m.catalogCancel()
		}
		m.phase = phaseAgent
		m.status = ""
		m.catalogFailed = false
		return m, nil
	case "F":
		if m.catalogFailed {
			if ag, err := m.cfg.AgentByName(m.agent); err == nil {
				return m.fetchCatalog(*ag)
			}
		}
	}
	return m, nil
}

// catalogHint is the key line under the loading and failed screens: only the
// keys that act (T5).
func (m model) catalogHint() string {
	back := "esc back"
	if m.initialAgent != "" {
		back = "esc quit"
	}
	if m.catalogFailed {
		return "F retry   " + back + "   ctrl+c quit"
	}
	return back + "   ctrl+c quit"
}

// catalogView is the loading screen or the failed-fetch screen. Both go
// through tuilayout.MessageAlone, so they fit any terminal (L13): an error too
// tall for the room is cut in its middle behind a marker, the hint last.
func (m model) catalogView() string {
	if m.width <= 0 || m.height <= 0 {
		return "model catalog (waiting for window size)"
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	body := "loading models from the model server…"
	if m.catalogFailed {
		errStyle := ErrorStyle(m.theme)
		lines := strings.Split(tuilayout.WrapText(m.status, m.width), "\n")
		for i, l := range lines {
			lines[i] = errStyle.Render(l)
		}
		body = strings.Join(lines, "\n")
	}
	return tuilayout.MessageAlone("", body, m.catalogHint(), m.width, m.height, dim)
}
