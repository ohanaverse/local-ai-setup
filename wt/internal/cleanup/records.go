package cleanup

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RecordRefPrefix is where wt rescue keeps its records: one ref per rescue,
// named by the first 16 hex digits of the record commit. It repeats the
// constant in internal/rescue, which this package cannot import.
const RecordRefPrefix = "refs/wt/rescued/"

// The trailer keys a record commit carries (see internal/rescue).
const (
	trailerVersion    = "Wt-Version"
	trailerSourcePath = "Wt-Source-Path"
	trailerSourceRef  = "Wt-Source-Ref"
	trailerSourceHead = "Wt-Source-Head"
	trailerDestBranch = "Wt-Dest-Branch"
	trailerDestPath   = "Wt-Dest-Path"
)

// recordKeys are the trailers read after the version, in field order.
var recordKeys = []string{
	trailerSourcePath, trailerSourceRef, trailerSourceHead, trailerDestBranch, trailerDestPath,
}

// RescueRecord is one parsed record: the ref, its commit and the trailer
// values. It says what a rescue wrote, not that the rescue still covers any
// worktree; matching is a separate step.
type RescueRecord struct {
	Ref, Commit                                             string
	SourcePath, SourceRef, SourceHead, DestBranch, DestPath string
	// Created is the record commit's date (git's %(creatordate:unix)); zero
	// when it could not be read, which reads as an unknown age.
	Created time.Time
}

// ListRecords reads every ref under RecordRefPrefix with one git call and
// keeps the well-formed records: a commit whose Wt-Version is 1 and which has
// exactly one clean value for each other trailer. Anything else is dropped
// (it never errors the list). A failing git call returns an error and no
// records.
func ListRecords(g Git) ([]RescueRecord, error) {
	atom := func(key string) string {
		return "%(trailers:key=" + key + ",valueonly,unfold)%00"
	}
	format := "%(refname)%00%(objectname)%00%(objecttype)%00" + atom(trailerVersion)
	for _, k := range recordKeys {
		format += atom(k)
	}
	format += "%(creatordate:unix)%00"
	o, err := g.Run("for-each-ref", "--format="+format, RecordRefPrefix)
	if err != nil {
		return nil, err
	}
	if o.Code != 0 {
		return nil, fmt.Errorf("git for-each-ref %s: exit %d: %s", RecordRefPrefix, o.Code, strings.TrimSpace(o.Stderr))
	}
	var out []RescueRecord
	// Each ref ends with NUL then newline; trailer values end with newline.
	for _, chunk := range strings.Split(o.Stdout, "\x00\n") {
		chunk = strings.TrimPrefix(chunk, "\n")
		if chunk == "" {
			continue
		}
		f := strings.Split(chunk, "\x00")
		if len(f) != 3+1+len(recordKeys)+1 || f[2] != "commit" {
			continue
		}
		if v, ok := trailerValue(f[3]); !ok || v != "1" {
			continue
		}
		vals := make([]string, len(recordKeys))
		good := true
		for i := range recordKeys {
			if vals[i], good = trailerValue(f[4+i]); !good {
				break
			}
		}
		if !good {
			continue
		}
		rec := RescueRecord{
			Ref: f[0], Commit: f[1],
			SourcePath: vals[0], SourceRef: vals[1], SourceHead: vals[2],
			DestBranch: vals[3], DestPath: vals[4],
		}
		// A bad or missing date is an unknown age, never a dropped record.
		if secs, err := strconv.ParseInt(strings.TrimSpace(f[4+len(recordKeys)]), 10, 64); err == nil && secs > 0 {
			rec.Created = time.Unix(secs, 0)
		}
		out = append(out, rec)
	}
	return out, nil
}

// trailerValue reads one trailer atom's output: exactly one non-empty line
// with no control character. Several lines mean a duplicate, none a missing
// trailer.
func trailerValue(s string) (string, bool) {
	v := strings.TrimSuffix(s, "\n")
	if v == "" || strings.Contains(v, "\n") {
		return "", false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return v, true
}

// RecordMinAge is how old a rescue record must be before wt remove --record
// takes it with only the per-item approval. A younger record is listed
// recent and needs --accept-loss too: it is usually still the only copy of a
// just-rescued worktree's uncommitted work. (Decision of #543.)
const RecordMinAge = 7 * 24 * time.Hour

// The statuses of a RecordRow. The values are wire strings of the cleanup
// list's `records` section.
const (
	RecordUnused = "unused" // no worktree reads as rescued through it
	RecordInUse  = "in_use" // a worktree still matches it: never prunable
	RecordRecent = "recent" // younger than RecordMinAge, or of unknown age
)

// RecordRow is one rescue record as the list shows it. It is a separate
// section, not a Row Kind: the Kind set and the row contract stay frozen.
type RecordRow struct {
	Ref, ID, Commit                                         string
	SourcePath, SourceRef, SourceHead, DestBranch, DestPath string
	// AgeDays is the record's age in whole days; -1 when it is unknown.
	AgeDays int
	// SourceExists: a worktree lives at SourcePath. SourceMatches: it also
	// has the recorded ref and head (the record then still covers it).
	SourceExists, SourceMatches bool
	// DestExists: the destination branch is still a local branch.
	DestExists bool
	// InUse and InUseBy: a worktree (its path) reads as rescued through this
	// record, or matches it by identity.
	InUse   bool
	InUseBy string
	Status  string
	Reason  string
}

// RecordID is the short id of a record ref: the first recordIDLen digits of
// its name after RecordRefPrefix.
func RecordID(ref string) string { return strings.TrimPrefix(ref, RecordRefPrefix) }

// recordMatches reports whether worktree w is the one record r was taken
// from: the same resolved path, the same branch (or "detached") and the same
// head, which is what preservedOf requires of a record before it covers
// anything.
func recordMatches(r RescueRecord, w WorktreeFact) bool {
	ref := "detached"
	if w.Branch != "" {
		ref = w.Branch
	} else if !w.Detached {
		return false
	}
	return w.Head != "" && r.SourcePath == resolve(w.Path) && r.SourceRef == ref && r.SourceHead == w.Head
}

// RecordUsers lists, from a fresh read of the worktrees, the paths of the
// worktrees record r still matches (see recordMatches). Pruning a record
// re-checks with it; an error means the answer is unknown and nothing may be
// pruned. Read-only.
func RecordUsers(g Git, cwd string, r RescueRecord) ([]string, error) {
	facts, err := ListWorktrees(g, cwd)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, w := range facts {
		if recordMatches(r, w) {
			out = append(out, w.Path)
		}
	}
	return out, nil
}

// RecordRows builds one row per record in s, in the order of s.Records. It
// is pure: it reads the snapshot's worktrees (including the Preserved ref a
// worktree read), heads and records, never git. A facts-only snapshot has
// read no records, so it yields none.
func RecordRows(s Snapshot, now time.Time) []RecordRow {
	var out []RecordRow
	for _, r := range s.Records {
		row := RecordRow{
			Ref: r.Ref, ID: RecordID(r.Ref), Commit: r.Commit,
			SourcePath: r.SourcePath, SourceRef: r.SourceRef, SourceHead: r.SourceHead,
			DestBranch: r.DestBranch, DestPath: r.DestPath, AgeDays: -1,
		}
		if !r.Created.IsZero() && !now.Before(r.Created) {
			row.AgeDays = int(now.Sub(r.Created) / (24 * time.Hour))
		}
		for _, ws := range s.Worktrees {
			w := ws.Fact
			if resolve(w.Path) == r.SourcePath {
				row.SourceExists = true
				row.SourceMatches = recordMatches(r, w)
			}
			if recordMatches(r, w) || (ws.Preserved != nil && ws.Preserved.Ref == r.Ref) {
				row.InUse, row.InUseBy = true, w.Path
			}
		}
		for _, h := range s.Heads {
			if h.Ref == "refs/heads/"+r.DestBranch || h.Ref == r.DestBranch {
				row.DestExists = true
				break
			}
		}
		switch {
		case row.InUse:
			row.Status = RecordInUse
			row.Reason = "worktree " + flat(row.InUseBy) + " still matches this record"
		case r.Created.IsZero() || row.AgeDays < 0:
			row.Status = RecordRecent
			row.Reason = "age unknown"
		case now.Sub(r.Created) < RecordMinAge:
			row.Status = RecordRecent
			row.Reason = fmt.Sprintf("younger than %d days", int(RecordMinAge/(24*time.Hour)))
		default:
			row.Status = RecordUnused
			row.Reason = "no worktree matches it"
			if row.SourceExists {
				row.Reason = "its source worktree has moved on"
			}
		}
		out = append(out, row)
	}
	return out
}
