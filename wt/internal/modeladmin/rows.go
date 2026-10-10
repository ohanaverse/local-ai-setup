// Package modeladmin is the core of model management, shared by the `wt
// model` commands and the Models tab of `wt config`: the rows both list, the
// rules both validate with, and the registry writes both make. It imports no
// UI package, so the two cannot drift apart.
package modeladmin

import (
	"fmt"
	"os"
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Status says whether a row's model is there to be used.
type Status string

const (
	// StatusOK: a cloud model, or a local model whose weights the probe found.
	StatusOK Status = "ok"
	// StatusMissing: a registered local model the probe looked for and did
	// not find, or a local_path that is not there.
	StatusMissing Status = "missing"
	// StatusUnknown: the probe could not say — the provider was unreachable,
	// wt has no probe for it, or the row's location does not resolve.
	StatusUnknown Status = "unknown"
	// StatusNew: on disk (or pulled) and not in the registry.
	StatusNew Status = "new"
	// StatusNone: an mlx_lm_server pairing, which no probe can enumerate.
	StatusNone Status = "-"
)

// The values of Row.Running.
const (
	RunningRun     = "run"  // serving now
	RunningLoad    = "load" // omlx is still loading it (#259)
	RunningNo      = ""     // not running, or a cloud model
	RunningUnknown = "?"    // the probe could not say
)

// Row is one model as model management lists it: a registry model, or a
// local model the probe found that the registry does not have.
type Row struct {
	ID         string
	Family     string
	ProviderID string
	ModelName  string
	Tags       []string
	// Location is "local" or "cloud", or "" for a registry row whose location
	// does not resolve (a gap the user repairs with `wt model edit`).
	Location   string
	Registered bool
	Status     Status
	Running    string
	// Path is where the weights are: the model's own directory as the
	// inventory reports it (omlx, mtplx — localmodels.Entry.Path, which
	// withholds a directory reached through another organization's copy of
	// the name), or the row's fetch.local_path. "" when wt does not know one.
	Path string
	// Size is the weights' size in bytes when the probe reports one (ollama);
	// 0 when it does not.
	Size int64
	Cost config.ModelCost
	// Target and Draft are the two sides of an mlx_lm_server pairing, each a
	// repo or a path; both "" for any other model.
	Target string
	Draft  string
	// Malformed names what the loader tolerated in the registry row
	// (config.Model.Malformed). In its fetch and draft: each reads as absent,
	// which is why the row has no path or no pairing side. And each
	// cost.time_prices row the registry's validator refuses, which the model
	// picker does not apply. Empty for a well-formed row and for a discovered
	// one.
	Malformed []string
}

// Pairing reports whether the row is an mlx_lm_server target+draft pairing.
func (r Row) Pairing() bool { return localmodels.RunningOnly(r.ProviderID) }

// statLocalPath reports whether a fetch.local_path is there. A seam: the
// answer is the file system's, and tests describe one.
var statLocalPath = realStatLocalPath

func realStatLocalPath(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Rows lists every registry model and every discovered local model, from one
// inventory snapshot. Unlike the pickers' rows (internal/catalog) nothing is
// hidden: a registered model that is not on disk, a stopped pairing and a row
// whose location does not resolve are all listed, because this is where the
// user repairs or removes them. Registry rows come first, by family and id;
// discovered rows follow, by id.
func Rows(cfg *config.Config, snap localmodels.Snapshot) []Row {
	// A queue per provider and id, not one entry: this list runs on a
	// registry Validate rejects, and a duplicated id is one of the gaps it
	// shows. The inventory has one entry per local registry model, and only
	// the first of two rows that name the same weights is matched to them;
	// one entry for both would give both the last one's status, and an
	// on-disk model would read "missing" twice. The provider is in the key
	// because the inventory sorts its entries by provider id (a stable sort):
	// entries of one provider and id keep the registry's order, entries of
	// one id under two providers do not, and a queue by id alone would hand
	// each of those rows the other's entry.
	entryKey := func(providerID, id string) string { return providerID + "\x00" + id }
	entries := map[string][]localmodels.Entry{}
	for _, e := range snap.Entries {
		if e.Registered {
			k := entryKey(e.ProviderID, e.ModelID)
			entries[k] = append(entries[k], e)
		}
	}
	var rows []Row
	for _, m := range cfg.Models {
		r := Row{
			ID: m.ID, Family: m.Family, ProviderID: m.ProviderID, ModelName: m.ModelName,
			Tags: m.Tags, Registered: true, Cost: m.Cost, Malformed: m.Malformed(),
		}
		loc, err := cfg.ResolveLocation(m)
		switch {
		case err != nil:
			r.Status = StatusUnknown
		case loc == config.LocationCloud:
			r.Location, r.Status = string(loc), StatusOK
		default:
			r.Location = string(loc)
			var e localmodels.Entry
			k := entryKey(m.ProviderID, m.ID)
			probed := len(entries[k]) > 0
			if probed {
				e, entries[k] = entries[k][0], entries[k][1:]
			}
			r.Running = running(snap, e, probed)
			switch {
			case r.Pairing():
				r.Status = StatusNone
				r.Target, r.Draft = m.Fetch.Target(), m.Draft.Target()
			case m.Fetch.LocalPath != "":
				// The user's own directory, outside the provider's model
				// directory: the scan cannot find it, a stat can.
				r.Path = m.Fetch.LocalPath
				if p, err := config.ExpandHome(m.Fetch.LocalPath); err == nil {
					r.Path = p
				}
				r.Status = StatusMissing
				if statLocalPath(r.Path) {
					r.Status = StatusOK
				}
			case !probed, !e.ArtifactKnown:
				r.Status = StatusUnknown
			case e.Artifact == "":
				r.Status = StatusMissing
			default:
				// The path as the inventory gives it: already the model's own
				// directory or "" (localmodels.Entry.Path, #266).
				r.Status, r.Path, r.Size = StatusOK, e.Path, e.Size
			}
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Family != rows[j].Family {
			return rows[i].Family < rows[j].Family
		}
		return rows[i].ID < rows[j].ID
	})
	// A registry id is taken: a discovered artifact that would be listed
	// under one (a row omlx/Foo whose model_name is something else, beside an
	// on-disk Foo) is left out, as internal/catalog leaves it out of the
	// pickers. Two rows with one id would make `wt model rm <id>` and the
	// Models tab's cursor, which both go by id, act on the wrong one.
	taken := map[string]bool{}
	for _, m := range cfg.Models {
		taken[m.ID] = true
	}
	var found []Row
	for _, e := range snap.Entries {
		if e.Registered || taken[e.ModelID] {
			continue
		}
		found = append(found, Row{
			ID: e.ModelID, ProviderID: e.ProviderID, ModelName: e.ModelName,
			Location: string(config.LocationLocal), Status: StatusNew,
			Running: running(snap, e, true), Path: e.Path, Size: e.Size,
		})
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	return append(rows, found...)
}

// running is a local row's RUNNING cell. A family whose probe did not fully
// succeed reports Running flags nobody confirmed, so the cell is "?" rather
// than a blank that reads as "stopped" — except a server that refused the
// connection, where nothing is listening and "not running" is a fact. A
// provider wt has no probe for is "?" too: the inventory still lists its
// models, with a Running flag nothing ever set, and lifecycle.ProbeTrusted
// takes a family that was never probed for a trusted one.
func running(snap localmodels.Snapshot, e localmodels.Entry, probed bool) string {
	fam := localmodels.Family(e.ProviderID)
	if !probed || fam == "" {
		return RunningUnknown
	}
	switch {
	case e.Running && e.Loading:
		return RunningLoad
	case e.Running:
		return RunningRun
	case !lifecycle.ProbeTrusted(snap, fam) && !snap.Down[fam]:
		return RunningUnknown
	}
	return RunningNo
}

// WeightsNote says where a row's weights are, for the command that just
// removed it from the registry: wt never deletes weights, so the user is told
// what is left. "" for a row with nothing on this machine (a cloud model).
func WeightsNote(r Row) string {
	switch {
	case r.Location != string(config.LocationLocal):
		return ""
	case r.Pairing():
		return "target " + orDash(r.Target) + " and draft " + orDash(r.Draft) + " are untouched"
	case r.Path != "" && r.Status == StatusMissing:
		return "nothing was found at " + r.Path
	case r.Path != "":
		return "weights are still at " + r.Path
	case localmodels.Family(r.ProviderID) == "ollama" && (r.Status == StatusOK || r.Status == StatusNew):
		// Only when the probe saw it pulled: an ollama that could not be
		// asked (unknown) falls through to the could-not-tell line.
		return "still pulled in ollama (`ollama rm " + r.ModelName + "` deletes it)"
	case r.Status == StatusMissing:
		return "no weights were found on disk"
	}
	return "wt could not tell where its weights are"
}

// FormatSize renders a byte count the way `ollama list` does: decimal units,
// one decimal place from a gigabyte up. 0 (not known) is "-". A unit starts
// where the one below it would round to 1000, so no size prints as "1000 MB".
func FormatSize(n int64) string {
	switch {
	case n <= 0:
		return "-"
	case n >= 999_500_000:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 999_500:
		return fmt.Sprintf("%.0f MB", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0f kB", float64(n)/1e3)
	}
	return fmt.Sprintf("%d B", n)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
