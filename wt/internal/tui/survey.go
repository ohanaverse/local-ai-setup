package tui

import (
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// emitPriceNotice is a seam for tests: production prints modelman's
// stale-pricing notice (issue #69) after the summary; tests swap it to
// observe ordering without touching the real modelman.toml.
var emitPriceNotice = realEmitPriceNotice

func realEmitPriceNotice() {
	agents.PrintPriceNotice()
}

// newSurveyStore is a seam for tests: production uses realNewSurveyStore
// (the default config dir); tests swap it to isolate from the real
// survey.jsonl, mirroring newUsageStore in model_list.go.
var newSurveyStore = realNewSurveyStore

func realNewSurveyStore() survey.Store { return survey.NewStore() }

// runSurvey is a seam for tests: production runs the real post-session
// survey prompt against the parent terminal; tests swap it to assert it
// was invoked with the right agent/model without needing a TTY. It returns
// the after-survey stats block, which the caller prints after the stop
// picker and summary; "" when nothing was surveyed (native model, no TTY).
var runSurvey = realRunSurvey

func realRunSurvey(agent string, m config.Model) string {
	return survey.PromptRun(os.Stdin, os.Stdout, newSurveyStore(), agent, m)
}

// runStopPhase is a seam for tests: production offers to stop running local
// models no other wt session uses (issue #115); tests swap it so nothing
// probes live servers or stops a real model.
var runStopPhase = realRunStopPhase

func realRunStopPhase(cfg *config.Config) {
	survey.Picker(os.Stdin, os.Stdout, cfg)
}

// releaseSession is a seam for tests: production releases this wt process's own
// refcount entry (survey.ReleaseSession) so the post-exit stop picker does not
// count the finished session as a user of its model. Best-effort.
var releaseSession = survey.ReleaseSession
