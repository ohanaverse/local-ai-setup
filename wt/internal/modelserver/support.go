package modelserver

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/candidates"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// candidates answers GET /control/v1/candidates?open_only=BOOL&provider=a,b&
// near=ID&per_provider=N: the registry's models ranked for a slot, with the
// tiers file's age. per_provider without near is a 400, as in the CLI.
func (s *Server) candidates(w http.ResponseWriter, r *http.Request) {
	qv := r.URL.Query()
	q := candidates.Query{Near: qv.Get("near")}
	if qv.Has("open_only") {
		v := qv.Get("open_only")
		open, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, modelapi.CodeBadRequest, fmt.Sprintf("open_only %q is not a boolean", v))
			return
		}
		q.OpenOnly = open
	}
	if qv.Has("per_provider") {
		if q.Near == "" {
			writeError(w, http.StatusBadRequest, modelapi.CodeBadRequest, "per_provider needs near")
			return
		}
		n, err := strconv.Atoi(qv.Get("per_provider"))
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, modelapi.CodeBadRequest, fmt.Sprintf("per_provider %q is not a non-negative integer", qv.Get("per_provider")))
			return
		}
		q.PerProvider = n
	}
	for _, p := range strings.Split(qv.Get("provider"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			q.Providers = append(q.Providers, p)
		}
	}

	out := modelapi.CandidateList{StaleSources: []string{}, Candidates: []modelapi.Candidate{}}
	// One read of the tiers file per request (the entries Rank ranks by come
	// from the same read as the freshness; a second parse would re-read a file
	// that may have changed and buy nothing — wt/CLAUDE.md's "read each input
	// file once per command").
	var ix tiers.Index
	if s.d.TiersFile != nil {
		var f tiers.Freshness
		var ok bool
		ix, f, ok = s.d.TiersFile()
		if ok {
			out.TiersGenerated = f.GeneratedAt.UTC().Format(time.RFC3339)
			if len(f.StaleSources) > 0 {
				out.StaleSources = f.StaleSources
			}
		}
	} else if s.d.Rank != nil {
		ix = s.d.Tiers()
	}
	if s.d.Rank != nil {
		for _, c := range s.d.Rank(s.d.Config(), ix, q) {
			out.Candidates = append(out.Candidates, wireCandidate(c))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func wireCandidate(c candidates.Candidate) modelapi.Candidate {
	wc := modelapi.Candidate{
		ID: c.ID, Provider: c.Provider, Location: c.Location, Tier: c.Tier, Confidence: c.Confidence,
		Rated: c.Rated, OpenWeights: c.OpenWeights, Context: c.Context,
		OutputPerMillion: c.OutputPerMillion, Subscription: c.Subscription, SubscriptionPeriod: c.SubscriptionPeriod,
		Flags: c.Flags,
	}
	if wc.Flags == nil {
		wc.Flags = []string{}
	}
	if c.HasScore {
		score := c.Score
		wc.Score = &score
	}
	return wc
}

// spend answers GET /control/v1/spend?start=RFC3339&end=RFC3339 from the
// LiteLLM spend log. A database nothing names is 503 spend_not_configured;
// any other failure is 503 spend_unavailable. Neither message carries the
// connection string (spend's own errors never do).
func (s *Server) spend(w http.ResponseWriter, r *http.Request) {
	qv := r.URL.Query()
	var bounds [2]time.Time
	for i, name := range []string{"start", "end"} {
		v := qv.Get(name)
		if v == "" {
			writeError(w, http.StatusBadRequest, modelapi.CodeBadRequest, name+" is required (an RFC 3339 time)")
			return
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, modelapi.CodeBadRequest, fmt.Sprintf("%s %q is not an RFC 3339 time", name, v))
			return
		}
		bounds[i] = t
	}
	if s.d.Spend == nil {
		writeError(w, http.StatusServiceUnavailable, modelapi.CodeSpendNotConfigured, modelapi.MsgSpendNotConfigured)
		return
	}
	res, err := s.d.Spend(r.Context(), bounds[0], bounds[1])
	if err != nil {
		if errors.Is(err, litellm.ErrNoDatabase) || errors.Is(err, spend.ErrNoConnectionString) {
			writeError(w, http.StatusServiceUnavailable, modelapi.CodeSpendNotConfigured, modelapi.MsgSpendNotConfigured)
			return
		}
		writeError(w, http.StatusServiceUnavailable, modelapi.CodeSpendUnavailable, fmt.Sprintf("spend unavailable: %v", err))
		return
	}
	rep := modelapi.SpendReport{Rows: []modelapi.SpendRow{}, Unattributed: res.Unattributed}
	for _, row := range res.Rows {
		rep.Rows = append(rep.Rows, modelapi.SpendRow(row))
	}
	writeJSON(w, http.StatusOK, rep)
}
