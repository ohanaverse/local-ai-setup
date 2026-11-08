package cleanup

import (
	"fmt"
	"strings"
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
		if len(f) != 3+1+len(recordKeys) || f[2] != "commit" {
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
		out = append(out, RescueRecord{
			Ref: f[0], Commit: f[1],
			SourcePath: vals[0], SourceRef: vals[1], SourceHead: vals[2],
			DestBranch: vals[3], DestPath: vals[4],
		})
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
