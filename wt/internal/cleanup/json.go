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
	Rows    []RowJSON  `json:"rows"`
	Error   *ErrorJSON `json:"error,omitempty"`
}

// BaseJSON is Base. FetchedAt is RFC 3339 UTC, absent when never fetched.
type BaseJSON struct {
	Default   string `json:"default"`
	Ref       string `json:"ref"`
	Fallback  bool   `json:"fallback"`
	FetchedAt string `json:"fetched_at,omitempty"`
}

// FetchJSON is FetchResult: whether `git fetch --prune` worked. Error is the
// failure text and is absent on success; At is RFC 3339 UTC.
type FetchJSON struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	At    string `json:"at,omitempty"`
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
// and Blockers are always arrays. AgeDays is Row.AgeDays (whole days, 0 when
// the age is unknown).
type RowJSON struct {
	Kind     string       `json:"kind"`
	Name     string       `json:"name"`
	Path     string       `json:"path,omitempty"`
	Branch   string       `json:"branch,omitempty"`
	Remote   string       `json:"remote,omitempty"`
	Status   string       `json:"status"`
	Evidence string       `json:"evidence,omitempty"`
	Unknown  bool         `json:"unknown"`
	AgeDays  int          `json:"age_days"`
	Reasons  []ReasonJSON `json:"reasons"`
	Blockers []ReasonJSON `json:"blockers"`
	PR       *PRJSON      `json:"pr,omitempty"`
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
func Encode(r Report) ([]byte, error) {
	e := Envelope{
		Version: EnvelopeVersion,
		Repo:    r.Repo,
		Base: &BaseJSON{
			Default: r.Base.Default, Ref: r.Base.Ref, Fallback: r.Base.Fallback,
			FetchedAt: timeJSON(r.Base.FetchedAt),
		},
		Fetch: &FetchJSON{OK: r.Fetch.OK, Error: r.Fetch.Err, At: timeJSON(r.Fetch.At)},
		GH:    string(r.GH),
		Rows:  make([]RowJSON, 0, len(r.Rows)),
	}
	for _, row := range r.Rows {
		rj := RowJSON{
			Kind: string(row.Kind), Name: row.Name, Path: row.Path, Branch: row.Branch,
			Remote: row.Remote, Status: string(row.Status), Evidence: string(row.Evidence),
			Unknown: row.Unknown, AgeDays: row.AgeDays,
			Reasons: reasonsJSON(row.Reasons), Blockers: reasonsJSON(row.Blockers),
		}
		if row.PR != nil {
			rj.PR = &PRJSON{Number: row.PR.Number, State: row.PR.State}
		}
		e.Rows = append(e.Rows, rj)
	}
	return marshal(e)
}

// EncodeError renders the failure envelope: the same document with Error
// set, no rows, and no base, fetch or gh.
func EncodeError(repo string, err error) ([]byte, error) {
	return marshal(Envelope{
		Version: EnvelopeVersion,
		Repo:    repo,
		Rows:    []RowJSON{},
		Error:   &ErrorJSON{Message: err.Error()},
	})
}
