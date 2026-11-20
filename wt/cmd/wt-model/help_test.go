package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
)

// TestHelpNamesSushiWhereItListsProviders pins the wt-model help texts that
// enumerate providers: the ones a registry write seeds (`wt-model model init`
// and `wt-model model add`) and the ones it stops (`wt-model stop`). Each must
// name sushi. Without it a built-in provider added later is silently missing
// from the help the way sushi was.
func TestHelpNamesSushiWhereItListsProviders(t *testing.T) {
	root := rootCmdWith(fakeDeps, testHost())
	for _, path := range [][]string{{"model", "init"}, {"model", "add"}, {"stop"}} {
		c, _, err := root.Find(path)
		if err != nil || c == nil || c.Name() != path[len(path)-1] {
			t.Fatalf("command %v not found: %v", path, err)
		}
		if !strings.Contains(c.Long, "sushi") {
			t.Errorf("%s: Long does not name sushi:\n%s", c.CommandPath(), c.Long)
		}
	}
}

// wtModelCommandFloor is the commands (the root included) wt-model has after
// phase 4 stage 2; lower it only when a command leaves on purpose.
const wtModelCommandFloor = 20

// staleNameRE matches an admin command written under its old wt name.
var staleNameRE = regexp.MustCompile(`(^|[^-\w/])wt (model( (init|list|add|edit|rm))?|cloud-sync|litellm (sync|list|providers|restart)|served|warm|stop (--all|<provider>|omlx|ollama|mtplx|rec|sushi|mlx_lm_server|llamacpp))([^/\w-]|$)`)

// staleHelpHits returns each stale old-name use in text; `wt model list --server` (a session command) is not one.
func staleHelpHits(text string) []string {
	var hits []string
	for _, m := range staleNameRE.FindAllStringSubmatchIndex(text, -1) {
		// m[3] ends the leading boundary group and m[5] the command group, so
		// text[m[3]:m[5]] is the old name alone and text[m[5]:] what follows it.
		if strings.HasPrefix(text[m[5]:], " --server") {
			continue
		}
		hits = append(hits, text[m[3]:m[5]])
	}
	return hits
}

// TestStaleHelpHitsSeesOnlyOldAdminNames pins the matcher the help walk below
// relies on: each admin command under its old wt name is a hit, while a
// session command (wt model list --server, wt stop <model>, a model id such as
// omlx/gemma, wt models, wt litellm status) and the new wt-model spelling are
// not, so the walk neither misses a stale name nor flags a correct one.
func TestStaleHelpHitsSeesOnlyOldAdminNames(t *testing.T) {
	for _, text := range []string{
		"run wt " + "model add x",
		"wt " + "cloud-sync --dry-run",
		"wt " + "stop omlx",
		"wt " + "litellm restart",
		"bare wt " + "model opens it",
	} {
		if len(staleHelpHits(text)) == 0 {
			t.Errorf("staleHelpHits(%q) found nothing, want a hit", text)
		}
	}
	for _, text := range []string{
		"wt model list --server",
		"wt-model model add",
		"wt stop <model>",
		"wt stop omlx/gemma",
		"wt models",
		"wt litellm status",
	} {
		if hits := staleHelpHits(text); len(hits) != 0 {
			t.Errorf("staleHelpHits(%q) = %q, want no hit", text, hits)
		}
	}
}

// TestWtModelHelpNamesNoOldCommand walks wt-model's whole command tree and
// fails on any help text (short, long, example, usage, and every flag's
// description) that still names an admin command by its old wt spelling: a
// reader who follows it lands on a pointer stub. The floor keeps the walk
// from passing vacuously when the tree is built wrong.
func TestWtModelHelpNamesNoOldCommand(t *testing.T) {
	seen := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		seen++
		path := c.CommandPath()
		for _, text := range []string{c.Short, c.Long, c.Example, c.UsageString()} {
			for _, hit := range staleHelpHits(text) {
				t.Errorf("%s: help names an old wt command: %q", path, hit)
			}
		}
		checkFlags := func(f *pflag.Flag) {
			for _, hit := range staleHelpHits(f.Usage) {
				t.Errorf("%s: help names an old wt command: %q", path, hit)
			}
		}
		c.Flags().VisitAll(checkFlags)
		c.PersistentFlags().VisitAll(checkFlags)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd(fakeDeps, serveRuntime{Record: servestate.Store{Path: filepath.Join(t.TempDir(), "serve.json")}}))
	if seen < wtModelCommandFloor {
		t.Fatalf("walked %d commands, want at least %d", seen, wtModelCommandFloor)
	}
}
