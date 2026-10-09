package cloudsync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// OffpeakLabel is the label of the time_prices row the ollama flow owns.
// Rows with any other label are the user's and are never touched.
const OffpeakLabel = "off-peak"

// CostUpdate is a price change to one existing entry.
type CostUpdate struct {
	ModelID, CatalogName string
	// Before is nil when the entry had no cost table.
	Before *Cost
	After  Cost
}

// Addition is an entry the plan adds.
type Addition struct {
	ID, Family, ModelName, CatalogName string
	Cost                               Cost
	// CloneOf is the id of the entry this one replaces under the tag ollama
	// publishes, or "". The new row is a copy of that one, so its hand-set
	// keys, tags and model_info survive the id change.
	CloneOf string
}

// Pull is one `ollama pull`: the registry id the plan lists and the tag the
// CLI is given.
type Pull struct{ ID, Tag string }

// CatalogPlan is what a sync changes so that `ollama list`'s cloud stubs and
// the registry's ollama cloud entries both mirror the page.
type CatalogPlan struct {
	CatalogSize int
	// CloudEntries counts the registry's ollama cloud entries before the sync.
	CloudEntries int
	Updates      []CostUpdate
	Unchanged    []string
	Additions    []Addition
	// Pulls are the entries, existing or added, whose tag `ollama list` lacks.
	Pulls []Pull
	// Removals are the ids of ollama cloud entries the page no longer lists,
	// or lists under another tag. Each is removed from the registry whether or
	// not it is pulled; its tag is removed from ollama only when it is a cloud
	// tag (Apply), and a removal whose tag is not one carries a warning.
	Removals []string
	// StrayTags are pulled cloud tags neither on the page nor in the registry.
	StrayTags []string
	// Replaced maps a removed id to the id it is re-added under.
	Replaced map[string]string
	Warnings []string
}

// RemovedCount is how many ollama cloud entries the plan takes out of the
// registry for good: its removals less the re-tagged ones, which come
// straight back under another id. It is the number the mass-removal guard
// weighs and the number its refusal prints, so the two cannot disagree; the
// printed plan's removal list and the removal digest cover every removal.
func (p *CatalogPlan) RemovedCount() int {
	gone := 0
	for _, id := range p.Removals {
		if _, retagged := p.Replaced[id]; !retagged {
			gone++
		}
	}
	return gone
}

// MassRemoval reports whether more than half the registry's ollama cloud
// entries would go (RemovedCount of CloudEntries), which is more likely a
// page that parsed wrong than a catalog that shrank.
func (p *CatalogPlan) MassRemoval() bool {
	return p.RemovedCount()*2 > p.CloudEntries
}

// RemovalDigest fingerprints everything the plan deletes: registry removals
// and stray `ollama rm`s. It is "" when the plan deletes nothing. `--yes`
// applies deletions only under the digest a reviewed dry run printed, so a
// page that changed since cannot delete a model nobody saw.
//
// The digest is a fixed format, because a printed digest approves the same
// plan on a later run (the vectors are in catalog_test.go): the first 12 hex
// digits of the SHA-256 of the sorted removals followed by the sorted
// "rm <tag>" lines, joined with newlines.
func (p *CatalogPlan) RemovalDigest() string {
	items := slices.Clone(p.Removals)
	sort.Strings(items)
	strays := make([]string, len(p.StrayTags))
	for i, t := range p.StrayTags {
		strays[i] = "rm " + t
	}
	sort.Strings(strays)
	items = append(items, strays...)
	if len(items) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(items, "\n")))
	return hex.EncodeToString(sum[:])[:12]
}

// HasWork reports whether applying the plan would change anything, in the
// registry or in ollama.
func (p *CatalogPlan) HasWork() bool {
	return len(p.Updates)+len(p.Additions)+len(p.Pulls)+len(p.Removals)+len(p.StrayTags) > 0
}

func isOllamaCloud(e Entry) bool {
	return e.ProviderID == ollamaProvider && (e.Location == "cloud" || IsCloudTag(e.ModelName))
}

func stem(tag string) string {
	name, _, _ := strings.Cut(tag, ":")
	return name
}

// findEntry returns the entry for page model name whose cloud tag is tag
// ("": unknown), or nil. An entry already under the tag wins over one
// carrying the catalog name, so an entry an earlier sync added under a
// guessed tag cannot shadow it. With the tag unknown it falls back to the
// guessed tag, then to the only ollama cloud entry with the same name stem.
func findEntry(entries []Entry, name, tag string) *Entry {
	first := func(match func(Entry) bool) *Entry {
		for i := range entries {
			if entries[i].ProviderID == ollamaProvider && match(entries[i]) {
				return &entries[i]
			}
		}
		return nil
	}
	if tag != "" {
		if hit := first(func(e Entry) bool { return e.ModelName == tag }); hit != nil {
			return hit
		}
	}
	if hit := first(func(e Entry) bool { return e.CatalogName == name }); hit != nil {
		return hit
	}
	if tag != "" {
		return nil
	}
	guess := CloudTag(name)
	if hit := first(func(e Entry) bool { return e.ModelName == guess }); hit != nil {
		return hit
	}
	var kin []*Entry
	for i := range entries {
		if isOllamaCloud(entries[i]) && stem(entries[i].ModelName) == stem(name) {
			kin = append(kin, &entries[i])
		}
	}
	if len(kin) == 1 {
		return kin[0]
	}
	return nil
}

// merged is page with each unrecognized price replaced by the old one.
func merged(page PriceTriple, oldInput, oldCache, oldOutput *float64) (input, cache, output *float64) {
	input, cache, output = page.Input, page.Cache, page.Output
	if page.Unknown.Input {
		input = oldInput
	}
	if page.Unknown.Cache {
		cache = oldCache
	}
	if page.Unknown.Output {
		output = oldOutput
	}
	return input, cache, output
}

// offpeakRow is ollama's published off-peak window as a time_prices row:
// outside 12:00 to 18:00 UTC on weekdays, and all day at weekends. The keys
// are in schema order. The window is not parsed from the page; if ollama
// changes it, change it here.
func offpeakRow(input, cache, output *float64) *tomlw.Table {
	window := func(start, end string, days ...string) *tomlw.Table {
		w := tomlw.NewTable()
		list := make([]any, len(days))
		for i, d := range days {
			list[i] = d
		}
		w.Set("days", list)
		w.Set("start", start)
		w.Set("end", end)
		return w
	}
	row := tomlw.NewTable()
	row.Set("label", OffpeakLabel)
	row.Set("timezone", "UTC")
	for _, kv := range []struct {
		key string
		v   *float64
	}{{"input_price_per_million", input}, {"cache_price_per_million", cache}, {"output_price_per_million", output}} {
		if kv.v != nil {
			row.Set(kv.key, *kv.v)
		}
	}
	row.Set("windows", []any{
		window("00:00", "12:00", "mon", "tue", "wed", "thu", "fri"),
		window("18:00", "24:00", "mon", "tue", "wed", "thu", "fri"),
		window("00:00", "24:00", "sat", "sun"),
	})
	return row
}

func isOffpeak(row *tomlw.Table) bool { return str(row, "label") == OffpeakLabel }

// withCatalogPrices is existing with the page's prices for cm. The off-peak
// row is replaced where it stands: time_prices resolve first match wins, so
// moving it would change which row wins an overlapping window. Every other
// row, the subscription and any key this package does not read are kept.
func withCatalogPrices(existing *Cost, cm CatalogModel) Cost {
	var base Cost
	if existing != nil {
		base = *existing
	}
	at, old := -1, (*tomlw.Table)(nil)
	var rows []*tomlw.Table
	for i, row := range base.TimePrices {
		if !isOffpeak(row) {
			rows = append(rows, row)
		} else if old == nil {
			at, old = i, row
		}
	}
	if cm.Offpeak != nil {
		var oldIn, oldCache, oldOut *float64
		if old != nil {
			oldIn, oldCache, oldOut = number(old, "input_price_per_million"), number(old, "cache_price_per_million"), number(old, "output_price_per_million")
		}
		if at < 0 || at > len(rows) {
			at = len(rows)
		}
		rows = slices.Insert(rows, at, offpeakRow(merged(*cm.Offpeak, oldIn, oldCache, oldOut)))
	}
	after := base
	after.Input, after.Cache, after.Output = merged(cm.Prices, base.Input, base.Cache, base.Output)
	after.TimePrices = rows
	return after
}

// sameCost reports whether a sync would leave the cost as it is. A missing
// cost table and one with no prices are the same thing here.
func sameCost(before *Cost, after Cost) bool {
	var b Cost
	if before != nil {
		b = *before
	}
	return samePrice(b.Input, after.Input) && samePrice(b.Cache, after.Cache) && samePrice(b.Output, after.Output) &&
		sameRows(b.TimePrices, after.TimePrices)
}

// sameRows reports whether two time_prices lists hold equal rows in the same
// order.
func sameRows(a, b []*tomlw.Table) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !tomlw.Same(a[i], b[i]) {
			return false
		}
	}
	return true
}

func familyFor(entries []Entry, name string) string {
	for _, e := range entries {
		if e.ProviderID == ollamaProvider && stem(e.ModelName) == stem(name) {
			return e.Family
		}
	}
	return stem(name)
}

// sharedSubscription is the subscription every ollama cloud entry agrees on,
// which a new entry inherits. An entry with no cost table counts as "none".
// When they disagree a new entry gets none, and disagree is true.
func sharedSubscription(entries []Entry) (price *float64, period *string, disagree bool) {
	seen := false
	for _, e := range entries {
		if !isOllamaCloud(e) {
			continue
		}
		var p *float64
		var per *string
		if e.Cost != nil {
			p, per = e.Cost.SubscriptionPrice, e.Cost.SubscriptionPeriod
		}
		if !seen {
			price, period, seen = p, per, true
			continue
		}
		samePeriod := (per == nil) == (period == nil) && (per == nil || *per == *period)
		if !samePrice(p, price) || !samePeriod {
			return nil, nil, true
		}
	}
	return price, period, false
}

// PlanCatalog works out what a sync would change. entries are the registry's
// model rows, pulled is `ollama list`'s NAME column, and resolved is
// ResolveCloudTags' map.
//
// A nil resolved means "trust CloudTag": no tag was verified, so no entry is
// ever re-tagged. In a non-nil map, "" (or a missing name) means the model's
// cloud tag is unknown: an existing entry still gets its prices, nothing is
// added or pulled for it, and nothing that may be it (an entry or a pulled
// cloud tag with its name) is removed.
//
// It is pure and deterministic: the same arguments give the same plan, which
// is what lets the command re-plan under the registry lock and compare.
func PlanCatalog(entries []Entry, catalog Catalog, pulled []string, resolved map[string]string) *CatalogPlan {
	plan := &CatalogPlan{
		CatalogSize: len(catalog.Models),
		Replaced:    map[string]string{},
		Warnings:    slices.Clone(catalog.Warnings),
	}
	ids := map[string]bool{}
	for _, e := range entries {
		ids[e.ID] = true
		if isOllamaCloud(e) {
			plan.CloudEntries++
		}
	}
	isPulled := func(tag string) bool { return slices.Contains(pulled, tag) }
	matched := map[string]bool{}
	listed := map[string]bool{}
	subPrice, subPeriod, disagree := sharedSubscription(entries)
	if disagree {
		plan.Warnings = append(plan.Warnings, "ollama cloud entries disagree on subscription pricing; new entries get none")
	}

	for _, cm := range catalog.Models {
		tag := CloudTag(cm.Name)
		if resolved != nil {
			tag = resolved[cm.Name]
		}
		entry := findEntry(entries, cm.Name, tag)
		var retagged *Entry
		if resolved != nil && tag != "" && entry != nil && entry.ModelName != tag {
			// Its tag is not what ollama publishes (an earlier sync guessed):
			// left unmatched, so it is removed and re-added under the real
			// tag below.
			retagged, entry = entry, nil
		}
		if tag == "" {
			// Still on the page, just unresolved: protect whatever may be it.
			for _, e := range entries {
				if isOllamaCloud(e) && stem(e.ModelName) == stem(cm.Name) {
					matched[e.ID] = true
				}
			}
			listed[CloudTag(cm.Name)] = true
			for _, t := range pulled {
				if IsCloudTag(t) && stem(t) == stem(cm.Name) {
					listed[t] = true
				}
			}
		}
		if entry != nil {
			matched[entry.ID] = true
			// Both: the entry's tag and the canonical pulled tag are listed.
			listed[entry.ModelName] = true
			if tag != "" {
				listed[tag] = true
			}
			after := withCatalogPrices(entry.Cost, cm)
			if sameCost(entry.Cost, after) && entry.CatalogName == cm.Name {
				plan.Unchanged = append(plan.Unchanged, entry.ID)
			} else {
				plan.Updates = append(plan.Updates, CostUpdate{ModelID: entry.ID, CatalogName: cm.Name, Before: entry.Cost, After: after})
			}
			if tag != "" && !isPulled(entry.ModelName) {
				plan.Pulls = append(plan.Pulls, Pull{ID: entry.ID, Tag: entry.ModelName})
			}
			continue
		}
		if tag == "" {
			continue // unknown cloud tag; ResolveCloudTags warned
		}
		listed[tag] = true
		newID := ollamaProvider + "/" + tag
		if ids[newID] {
			where := "as an earlier addition from this page"
			for _, e := range entries {
				if e.ID != newID {
					continue
				}
				if e.ProviderID == ollamaProvider {
					// Single quotes, the form the plan has always printed this name in
					// (TestPlanIDCollisionWarns).
					where = fmt.Sprintf("with model_name '%s'", e.ModelName)
				} else {
					where = "on another provider"
				}
				break
			}
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s already exists %s; not adding", newID, where))
			continue
		}
		ids[newID] = true
		add := Addition{ID: newID, ModelName: tag, CatalogName: cm.Name}
		// A re-tag is the same model under the tag ollama publishes, so the
		// new entry is built off the one it replaces. (The LiteLLM route
		// name is the id, so that one cannot come across.)
		base := &Cost{SubscriptionPrice: subPrice, SubscriptionPeriod: subPeriod}
		if retagged != nil {
			plan.Replaced[retagged.ID] = newID
			add.CloneOf, add.Family = retagged.ID, retagged.Family
			if retagged.Cost != nil {
				base = retagged.Cost
			}
		} else {
			add.Family = familyFor(entries, cm.Name)
		}
		add.Cost = withCatalogPrices(base, cm)
		plan.Additions = append(plan.Additions, add)
		if !isPulled(tag) {
			plan.Pulls = append(plan.Pulls, Pull{ID: newID, Tag: tag})
		}
	}

	registered := map[string]bool{}
	for _, e := range entries {
		if e.ProviderID == ollamaProvider {
			registered[e.ModelName] = true
		}
		if isOllamaCloud(e) && !matched[e.ID] {
			plan.Removals = append(plan.Removals, e.ID)
			if !IsCloudTag(e.ModelName) {
				// An entry marked location = "cloud" over a tag that is not a
				// cloud stub: what `ollama rm` would delete is real weights.
				// Apply leaves the tag alone; the plan says so before anyone
				// approves it.
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: its tag %q is not a cloud tag; the entry is removed from the registry and the tag is left in ollama", e.ID, e.ModelName))
			}
		}
	}
	for _, t := range pulled {
		if IsCloudTag(t) && !listed[t] && !registered[t] {
			plan.StrayTags = append(plan.StrayTags, t)
		}
	}
	return plan
}

// formatPrice prints a price as Python's `{v:g}` does, so a plan reads the
// same from either tool: six significant digits, no trailing zeros, exponent
// form from 1e+06 up and below 0.0001.
func formatPrice(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'g', 6, 64)
}

func formatCost(c *Cost) string {
	if c == nil {
		return "no cost"
	}
	text := formatPrice(c.Input) + "/" + formatPrice(c.Cache) + "/" + formatPrice(c.Output)
	for _, row := range c.TimePrices {
		if isOffpeak(row) {
			text += fmt.Sprintf(" (off-peak %s/%s/%s)", formatPrice(number(row, "input_price_per_million")),
				formatPrice(number(row, "cache_price_per_million")), formatPrice(number(row, "output_price_per_million")))
			break
		}
	}
	return text
}

// Format prints the plan, one section per kind of change. It is the text the
// command prints for review, and compares with the re-plan made under the
// registry lock: two plans that print the same are the same plan.
func (p *CatalogPlan) Format() string {
	lines := []string{fmt.Sprintf("ollama.com/pricing: %d models (prices are input/cached/output per million tokens)", p.CatalogSize)}
	lines = append(lines, fmt.Sprintf("Price updates (%d):", len(p.Updates)))
	for _, u := range p.Updates {
		lines = append(lines, fmt.Sprintf("  %s: %s -> %s", u.ModelID, formatCost(u.Before), formatCost(&u.After)))
	}
	lines = append(lines, fmt.Sprintf("Registry additions (%d):", len(p.Additions)))
	for _, a := range p.Additions {
		lines = append(lines, fmt.Sprintf("  %s [family %s]: %s", a.ID, a.Family, formatCost(&a.Cost)))
	}
	lines = append(lines, fmt.Sprintf("Unchanged prices: %d", len(p.Unchanged)))
	lines = append(lines, fmt.Sprintf("ollama pull (%d):", len(p.Pulls)))
	for _, pull := range p.Pulls {
		lines = append(lines, "  "+pull.ID)
	}
	lines = append(lines, fmt.Sprintf("Registry removals — off ollama.com/pricing or under a tag ollama doesn't publish; `ollama rm` if pulled (%d):", len(p.Removals)))
	for _, id := range p.Removals {
		line := "  " + id
		if to, ok := p.Replaced[id]; ok {
			line += " (re-tagged as " + to + ")"
		}
		lines = append(lines, line)
	}
	lines = append(lines, fmt.Sprintf("ollama rm — pulled, unregistered, off the page (%d):", len(p.StrayTags)))
	for _, tag := range p.StrayTags {
		lines = append(lines, "  "+tag)
	}
	if digest := p.RemovalDigest(); digest != "" {
		lines = append(lines, fmt.Sprintf("Removal digest: %s (apply non-interactively with `--yes --approve-removals %s`)", digest, digest))
	}
	for _, w := range p.Warnings {
		lines = append(lines, "warning: "+w)
	}
	return strings.Join(lines, "\n")
}
