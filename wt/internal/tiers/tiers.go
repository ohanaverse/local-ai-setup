// Package tiers reads the advisory capability tiers modeltiers writes
// (`tiers.toml`) so wt's model picker can show them as guidance. wt only
// reads the file: it never writes it, computes a tier, or routes on one.
//
// The file's format is the contract fixture docs/contracts/tiers.sample.toml
// at the monorepo root. Its rule for readers is modeltiers' own: a model
// absent from the file, an unrated entry, and a file that is missing or
// cannot be parsed all mean "no rating", and none of them is an error a
// caller has to report.
package tiers

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// OutEnv names the file in place of the default path, exactly as it does for
// `modeltiers run` (modeltiers/src/modeltiers/paths.py, OUT_ENV).
const OutEnv = "MODELTIERS_OUT"

// Path is where modeltiers writes tiers.toml, by its own rule
// (paths.output_path): MODELTIERS_OUT when it is set and not empty, else
// `$XDG_CONFIG_HOME/local-ai/tiers.toml` when that variable is set and not
// empty, else `~/.config/local-ai/tiers.toml`. It is "" only when no home
// directory can be found.
func Path() string {
	if p := os.Getenv(OutEnv); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "local-ai", "tiers.toml")
}

// Entry is one `[[models]]` entry of the file: the fields the picker shows.
// Every other key of the file is left unread.
type Entry struct {
	ProviderID string
	ModelName  string
	// Tier is the tier's name: marathon, deep, standard, quick or unrated.
	Tier string
	// Code is the tier's one-letter code (M, D, S, Q), or "-" when unrated.
	Code string
	// Confidence is high, low or none.
	Confidence string
	// Aligned is the aligned score, the advisory estimate on the
	// intelligence-index scale; HasAligned is false when the entry has none.
	Aligned    float64
	HasAligned bool
	// Sources is how many voting scores the aligned score was made from
	// (len(aligned_sources)).
	Sources int
}

// Rated reports whether the entry carries a tier the picker shows: a code of
// M, D, S or Q on an entry whose tier is not unrated.
func (e Entry) Rated() bool {
	if e.Tier == "unrated" {
		return false
	}
	switch e.Code {
	case "M", "D", "S", "Q":
		return true
	}
	return false
}

// score is the aligned score rounded to a whole number, as the picker writes
// it.
func (e Entry) score() int { return int(math.Round(e.Aligned)) }

// Short is the picker's TIER cell: the code followed by the rounded aligned
// score ("S44"), the code alone when the entry has no aligned score, and "-"
// for an entry that is not rated (the zero Entry included).
func (e Entry) Short() string {
	if !e.Rated() {
		return "-"
	}
	if !e.HasAligned {
		return e.Code
	}
	return fmt.Sprintf("%s%d", e.Code, e.score())
}

// Notes are the long forms of the entry for the picker's mode line, longest
// first, each one shorter than the one before:
//
//	tier standard · score 44 (3 sources) · high confidence
//	tier standard · score 44 (3 sources)
//	tier standard · score 44
//	tier standard
//
// A part the entry does not have is left out (no aligned score, a confidence
// of none), and a form that would equal the one before it is not repeated.
// nil for an entry that is not rated.
func (e Entry) Notes() []string {
	if !e.Rated() {
		return nil
	}
	tier := "tier " + e.Tier
	if e.Tier == "" {
		tier = "tier " + e.Code
	}
	score := ""
	sources := ""
	if e.HasAligned {
		score = fmt.Sprintf("score %d", e.score())
		switch {
		case e.Sources == 1:
			sources = " (1 source)"
		case e.Sources > 1:
			sources = fmt.Sprintf(" (%d sources)", e.Sources)
		}
	}
	conf := ""
	if e.Confidence != "" && e.Confidence != "none" {
		conf = e.Confidence + " confidence"
	}
	join := func(parts ...string) string {
		var kept []string
		for _, p := range parts {
			if p != "" {
				kept = append(kept, p)
			}
		}
		return strings.Join(kept, " · ")
	}
	var out []string
	for _, n := range []string{
		join(tier, score+sources, conf),
		join(tier, score+sources),
		join(tier, score),
		tier,
	} {
		if len(out) == 0 || out[len(out)-1] != n {
			out = append(out, n)
		}
	}
	return out
}

// key is how an entry is found: the inventory's provider_id and model_name,
// the two fields of a wt model row (config.Model.ProviderID and ModelName)
// that `wt model list --json` writes and modeltiers keys its entries by.
type key struct{ provider, name string }

// Index is the file's entries by provider and provider-side model name. The
// zero Index (nil) has no entries; every lookup on it finds nothing.
type Index map[key]Entry

// Lookup returns the entry for a model, and whether the file has one.
func (ix Index) Lookup(providerID, modelName string) (Entry, bool) {
	e, ok := ix[key{providerID, modelName}]
	return e, ok
}

// Rating returns the entry for a model when the file has one that is rated,
// and the zero Entry (Short "-", no Notes) otherwise.
func (ix Index) Rating(providerID, modelName string) Entry {
	if e, ok := ix.Lookup(providerID, modelName); ok && e.Rated() {
		return e
	}
	return Entry{}
}

// fileDoc is the part of tiers.toml this package reads. aligned_score is
// decoded as any so that an integer written for it reads as well as a float.
type fileDoc struct {
	Models []struct {
		ProviderID     string   `toml:"provider_id"`
		ModelName      string   `toml:"model_name"`
		Tier           string   `toml:"tier"`
		Code           string   `toml:"code"`
		Confidence     string   `toml:"confidence"`
		AlignedScore   any      `toml:"aligned_score"`
		AlignedSources []string `toml:"aligned_sources"`
	} `toml:"models"`
}

// Parse reads the text of a tiers.toml. An entry with no provider_id or no
// model_name is skipped; when two entries name the same model the first one
// is kept. An error means the text is not a tiers file this package can read,
// which a caller treats as no file at all.
func Parse(data []byte) (Index, error) {
	var doc fileDoc
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return nil, err
	}
	ix := Index{}
	for _, m := range doc.Models {
		if m.ProviderID == "" || m.ModelName == "" {
			continue
		}
		k := key{m.ProviderID, m.ModelName}
		if _, dup := ix[k]; dup {
			continue
		}
		e := Entry{
			ProviderID: m.ProviderID,
			ModelName:  m.ModelName,
			Tier:       m.Tier,
			Code:       m.Code,
			Confidence: m.Confidence,
			Sources:    len(m.AlignedSources),
		}
		switch v := m.AlignedScore.(type) {
		case float64:
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				e.Aligned, e.HasAligned = v, true
			}
		case int64:
			e.Aligned, e.HasAligned = float64(v), true
		}
		ix[k] = e
	}
	return ix, nil
}

// maxFileSize bounds what Load reads: the file holds one short entry per
// model, so anything near this size is not one modeltiers wrote.
const maxFileSize = 16 << 20

// Load reads the file at Path and returns its entries. It never fails: a
// missing, unreadable, oversized or unparseable file is an empty Index, as
// modeltiers' rule for readers says, so the picker shows no rating and says
// nothing about it.
func Load() Index {
	return LoadFile(Path())
}

// LoadFile is Load for a named file.
func LoadFile(path string) Index {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileSize {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	ix, err := Parse(data)
	if err != nil {
		return nil
	}
	return ix
}
