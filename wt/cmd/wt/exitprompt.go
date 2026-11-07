package main

import (
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitprompt"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
)

// exitPromptEnv is what the post-exit worktree prompt needs from this run:
// built once by newExitPromptEnv when a launch starts, read by every tail.
var exitPromptEnv exitprompt.Env

// newExitPromptEnv builds the prompt's Env from the loaded app. The gather
// inputs name the protected artifacts; a registry that cannot be read makes
// Inputs fail, so the offer prints its note and keeps the worktree.
func newExitPromptEnv(a *app) exitprompt.Env {
	return exitprompt.Env{
		Inputs: func(cwd string) (cleanup.GatherInputs, error) {
			protected, err := protectedPaths(a)
			if err != nil {
				return cleanup.GatherInputs{}, err
			}
			return cleanupInputs(cwd, false, protected, "wt-exit"), nil
		},
		Procs: removal.LsofFinder(),
	}
}

// offerExitPrompt is a seam for tests: production offers to remove the
// worktree and branch this launch created when nothing in them changed
// (exitprompt.Offer), after the stop picker and before the summary line.
// It never fails the launch.
var offerExitPrompt = realOfferExitPrompt

func realOfferExitPrompt(cfg *config.Config) {
	env := exitPromptEnv
	env.Keep = cfg.ExitPromptKeep()
	exitprompt.Offer(env, os.Stdin, os.Stdout, stdinTTY())
}
