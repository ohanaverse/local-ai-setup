package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// TestStartHasNoJSONOrPlanFlag pins the removal of `wt start --json` and
// `--plan` (modelman was their only caller). A script that still passes one
// must be told so by name and exit non-zero with nothing started: a flag that
// was silently accepted and ignored would start a model, and possibly unload
// another, where the script asked for a dry run.
func TestStartHasNoJSONOrPlanFlag(t *testing.T) {
	for _, flag := range []string{"--json", "--plan"} {
		t.Run(flag, func(t *testing.T) {
			r := newRemoteRig(t, localRow("omlx/c", modelapi.StateStopped, modelapi.ActionStart, nil))
			c := startCmd(r.a)
			var out, errOut bytes.Buffer
			c.SetOut(&out)
			c.SetErr(&errOut)
			c.SetArgs([]string{"omlx/c", flag})
			err := c.Execute()
			if err == nil || !strings.Contains(err.Error(), "unknown flag: "+flag) {
				t.Fatalf("err = %v, want cobra's unknown flag: %s", err, flag)
			}
			if n := len(r.f.reqs()); n != 0 || out.Len() != 0 {
				t.Errorf("model server requests = %d, stdout = %q; want nothing done", n, out.String())
			}
		})
	}
}
