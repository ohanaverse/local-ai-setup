package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestStartHasNoJSONOrPlanFlag pins the removal of `wt start --json` and
// `--plan` (modelman was their only caller). A script that still passes one
// must be told so by name and exit non-zero with nothing started: a flag that
// was silently accepted and ignored would start a model, and possibly unload
// another, where the script asked for a dry run.
func TestStartHasNoJSONOrPlanFlag(t *testing.T) {
	for _, flag := range []string{"--json", "--plan"} {
		t.Run(flag, func(t *testing.T) {
			cfg := loadingFixture(t)
			started := stubLifecycleStart(t, nil)
			driver := stubStartDriver(t, nil)
			c := startCmd(&app{cfg: cfg})
			var out, errOut bytes.Buffer
			c.SetOut(&out)
			c.SetErr(&errOut)
			c.SetArgs([]string{"omlx/c", flag})
			err := c.Execute()
			if err == nil || !strings.Contains(err.Error(), "unknown flag: "+flag) {
				t.Fatalf("err = %v, want cobra's unknown flag: %s", err, flag)
			}
			if len(*started) != 0 || driver.called || out.Len() != 0 {
				t.Errorf("starts = %d, driver called = %v, stdout = %q; want nothing done", len(*started), driver.called, out.String())
			}
		})
	}
}
