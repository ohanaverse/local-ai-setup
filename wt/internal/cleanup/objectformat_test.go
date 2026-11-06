package cleanup

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGatherRefusesSha256Repository: every proof and every removal check
// expects 40-hex commit ids, so in a sha256 repository each row read
// Unknown and each removal was refused one by one with a misleading reason
// ("commit seen ... is not a full commit id"). Gather says once, before
// anything else is read, that the object format is not supported.
func TestGatherRefusesSha256Repository(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	if b, err := exec.Command("git", "init", "-q", "--object-format=sha256", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Skipf("git cannot make a sha256 repository: %v\n%s", err, b)
	}
	if b, err := exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i").CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, b)
	}
	_, err := Gather(Git{Dir: dir}, GatherInputs{CWD: dir, OwnRepo: "o/r"})
	if err == nil || !strings.Contains(err.Error(), "sha256") || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("Gather error %v; want one naming the sha256 object format as not supported", err)
	}
}
