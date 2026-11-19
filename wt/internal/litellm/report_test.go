package litellm

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

func TestSyncAndWarnPrintsTheSyncLinesThenEveryWarning(t *testing.T) {
	var out, errOut bytes.Buffer
	sync := func(o, _ io.Writer) string { fmt.Fprintln(o, "synced"); return "LiteLLM routes not synced: x" }
	if err := SyncAndWarn(&out, &errOut, []string{"first"}, sync); err != nil {
		t.Fatal(err)
	}
	if out.String() != "synced\n" {
		t.Errorf("out = %q", out.String())
	}
	if errOut.String() != "warning: first\nwarning: LiteLLM routes not synced: x\n" {
		t.Errorf("errOut = %q", errOut.String())
	}
}

func TestSyncAfterWriteSkipsAMachineWithoutLiteLLM(t *testing.T) {
	t.Setenv("WT_LITELLM_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	probed := false
	got := SyncAfterWrite(io.Discard, io.Discard, func(*config.Config) localmodels.Snapshot { probed = true; return localmodels.Snapshot{} })
	if got != "" || probed {
		t.Fatalf("got %q, probed %v; want \"\" and no probe", got, probed)
	}
}
