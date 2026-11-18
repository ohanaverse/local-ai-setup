package modelmode

// Result is what an Apply did: the steps that ran, in order, and the one
// that failed with its error. Notes say what a step left behind (a backup's
// path); Recovery, after a failure that left the LiteLLM plist, wt-model's
// pair or a running service out of step, says what is true and the exact
// commands to run by hand. Nothing in it holds a secret.
type Result struct {
	Done     []Step
	Failed   *Step
	Err      error
	Warnings []string
	Notes    []string
	Recovery string
}
