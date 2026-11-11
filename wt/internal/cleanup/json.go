package cleanup

import (
	"bytes"
	"encoding/json"
	"time"
)

// EnvelopeVersion is the `version` of the --json document. Bump it for any
// change a consumer could trip on (a renamed key, a changed enum string);
// docs/contracts/cleanup-list.schema.json and the sample fixture move with it.
const EnvelopeVersion = 1

// Envelope is the one JSON document `wt cleanup --json` prints, on success
// and on failure. Rows is always present. On failure Error is set, Rows is
// empty and Base, Fetch and GH are absent.
type Envelope struct {
	Version int        `json:"version"`
	Repo    string     `json:"repo"`
	Base    *BaseJSON  `json:"base,omitempty"`
	Fetch   *FetchJSON `json:"fetch,omitempty"`
	GH      string     `json:"gh,omitempty"`
	// Notes are the caveats the text view prints under its header (a lowered
	// min-age, artifact protection off, no registry); absent when there are none.
	Notes []string   `json:"notes,omitempty"`
	Rows  []RowJSON  `json:"rows"`
	Error *ErrorJSON `json:"error,omitempty"`
}

// BaseJSON is Base. FetchedAt is RFC 3339 UTC, absent when no fetch ran or
// the fetch failed.
type BaseJSON struct {
	Default   string `json:"default"`
	Ref       string `json:"ref"`
	Fallback  bool   `json:"fallback"`
	FetchedAt string `json:"fetched_at,omitempty"`
}

// FetchJSON is FetchResult: whether `git fetch --prune origin` worked (the base is origin's; Remotes lists every remote). Error is the
// failure text and is absent on success; At is RFC 3339 UTC.
type FetchJSON struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	At    string `json:"at,omitempty"`
	// Remotes is every remote considered, origin included; absent when
	// origin is the only one (additive, optional).
	Remotes []FetchRemoteJSON `json:"remotes,omitempty"`
	// Skipped is why origin was not fetched ("no origin remote") while other
	// remotes were; absent otherwise (additive, optional).
	Skipped string `json:"skipped,omitempty"`
}

// FetchRemoteJSON is one remote's fetch. Skipped is a remote excluded by
// remote.<name>.skipFetchAll.
type FetchRemoteJSON struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

// ErrorJSON is the failure that ended the run.
type ErrorJSON struct {
	Message string `json:"message"`
}

// ReasonJSON is a Reason (also used for blockers).
type ReasonJSON struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// PRJSON is the PR a verdict rests on.
type PRJSON struct {
	Number int    `json:"number"`
	State  string `json:"state"`
}

// RowJSON is a Row. Evidence is absent when no proof was found; Unknown is
// the separate "could not be determined" flag and is always written. Reasons
// and Blockers are always arrays. AgeDays is Row.AgeDays (whole days, 0 for a
// known age under a day) and is absent when the age is unknown.
type RowJSON struct {
	Kind     string       `json:"kind"`
	Name     string       `json:"name"`
	Path     string       `json:"path,omitempty"`
	Branch   string       `json:"branch,omitempty"`
	Remote   string       `json:"remote,omitempty"`
	Status   string       `json:"status"`
	Evidence string       `json:"evidence,omitempty"`
	Unknown  bool         `json:"unknown"`
	AgeDays  *int         `json:"age_days,omitempty"`
	Reasons  []ReasonJSON `json:"reasons"`
	Blockers []ReasonJSON `json:"blockers"`
	PR       *PRJSON      `json:"pr,omitempty"`
	// HardStop is the code of the first blocker that refuses a removal
	// outright (Row.HardStop); absent when none does.
	HardStop string `json:"hard_stop,omitempty"`
	// Upstream is the remote ref folded into a local row (Row.Upstream);
	// absent when none is.
	Upstream *UpstreamJSON `json:"upstream,omitempty"`
}

// UpstreamJSON is an Upstream. Gone is a remote ref that no longer exists, and
// then tip, status and evidence are absent.
type UpstreamJSON struct {
	Ref      string `json:"ref"`
	Remote   string `json:"remote,omitempty"`
	Name     string `json:"name,omitempty"`
	Tip      string `json:"tip,omitempty"`
	Gone     bool   `json:"gone"`
	Status   string `json:"status,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

func reasonsJSON(rs []Reason) []ReasonJSON {
	out := make([]ReasonJSON, 0, len(rs))
	for _, r := range rs {
		out = append(out, ReasonJSON{Code: r.Code, Detail: r.Detail})
	}
	return out
}

func timeJSON(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func marshal(e Envelope) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Encode renders a report as the envelope, rows in the order Classify made.
// A remote ref folded into a local row is still its own row here (AllRows).
func Encode(r Report) ([]byte, error) {
	rows := r.AllRows()
	e := Envelope{
		Version: EnvelopeVersion,
		Repo:    r.Repo,
		GH:      string(r.GH),
		Notes:   NoteLines(r),
		Rows:    make([]RowJSON, 0, len(rows)),
	}
	// base is absent when no default branch was found; fetch is absent when
	// no fetch ran ("not attempted"), a zero At being the sign of that.
	// fetched_at is written only after a fetch that worked: after a failed
	// one (or none) the base is not known to be current as of any time.
	if r.Base.Default != "" {
		e.Base = &BaseJSON{Default: r.Base.Default, Ref: r.Base.Ref, Fallback: r.Base.Fallback}
		if r.Fetch.OK {
			e.Base.FetchedAt = timeJSON(r.Base.FetchedAt)
		}
	}
	if !r.Fetch.At.IsZero() || len(r.Fetch.Remotes) > 0 {
		// A repository with no origin has no At, but its other remotes were
		// fetched: the block carries ok false, skipped and their results.
		e.Fetch = &FetchJSON{OK: r.Fetch.OK, Error: r.Fetch.Err, Skipped: flat(r.Fetch.Skipped)}
		if !r.Fetch.At.IsZero() {
			e.Fetch.At = timeJSON(r.Fetch.At)
		}
		if len(r.Fetch.Remotes) > 1 || (len(r.Fetch.Remotes) == 1 && r.Fetch.Remotes[0].Name != "origin") {
			for _, rm := range r.Fetch.Remotes {
				e.Fetch.Remotes = append(e.Fetch.Remotes, FetchRemoteJSON{Name: flat(rm.Name), OK: rm.OK, Error: fetchErrText(rm.Err), Skipped: rm.Skipped})
			}
		}
	}
	for _, row := range rows {
		rj := RowJSON{
			Kind: string(row.Kind), Name: row.Name, Path: row.Path, Branch: row.Branch,
			Remote: row.Remote, Status: string(row.Status), Evidence: string(row.Evidence),
			Unknown: row.Unknown,
			Reasons: reasonsJSON(row.Reasons), Blockers: reasonsJSON(row.Blockers),
			HardStop: row.HardStop,
		}
		if u := row.Upstream; u != nil {
			rj.Upstream = &UpstreamJSON{Ref: u.Ref, Remote: u.Remote, Name: u.Name, Tip: u.Tip,
				Gone: u.Gone, Status: string(u.Status), Evidence: string(u.Evidence)}
		}
		if row.AgeKnown {
			days := row.AgeDays
			rj.AgeDays = &days
		}
		if row.PR != nil {
			rj.PR = &PRJSON{Number: row.PR.Number, State: row.PR.State}
		}
		e.Rows = append(e.Rows, rj)
	}
	return marshal(e)
}

// EncodeError renders the failure envelope: the same document with Error
// set, no rows, and no base, fetch or gh. A nil err is reported as
// "unknown error".
func EncodeError(repo string, err error) ([]byte, error) {
	msg := "unknown error" // a nil err still yields one document
	if err != nil {
		msg = err.Error()
	}
	return marshal(Envelope{
		Version: EnvelopeVersion,
		Repo:    repo,
		Rows:    []RowJSON{},
		Error:   &ErrorJSON{Message: msg},
	})
}
