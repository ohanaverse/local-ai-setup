// wt/internal/profiles/configcontent.go
package profiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// ConfigFileTarget returns the file profile config_content should be
// written to for agent, or ok=false if the agent takes config_content via
// an inline env var instead (opencode). claude's target is worktree-
// scoped (never the user's global ~/.claude/settings.json); codex's is a
// dedicated wt-owned global profile file, since codex has no
// project-scoped profile mechanism to use instead.
func ConfigFileTarget(agent, worktreePath string) (path string, isTOML bool, ok bool) {
	switch agent {
	case "claude":
		return filepath.Join(worktreePath, ".claude", "settings.local.json"), false, true
	case "codex":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".codex", "agent-wt-profile.config.toml"), true, true
	default:
		return "", false, false
	}
}

const openCodeConfigEnvPrefix = "OPENCODE_CONFIG_CONTENT="

// ApplyConfigContent writes rp.ConfigContent for agent: a real,
// snapshot/restored file for claude and codex, or a merge into cmd.Env's
// existing OPENCODE_CONFIG_CONTENT entry for opencode. It returns a
// cleanup function the caller MUST call after the launched process exits
// (success or failure) to restore whatever the write touched.
func ApplyConfigContent(cmd *exec.Cmd, agent, worktreePath string, rp ResolvedProfile) (cleanup func() error, err error) {
	noop := func() error { return nil }
	if len(rp.ConfigContent) == 0 {
		return noop, nil
	}
	if agent == "opencode" {
		if err := mergeOpenCodeEnv(cmd, rp.ConfigContent); err != nil {
			return nil, err
		}
		return noop, nil
	}
	path, isTOML, ok := ConfigFileTarget(agent, worktreePath)
	if !ok {
		return nil, fmt.Errorf("profile: agent %q has no config_content target", agent)
	}
	var data []byte
	if isTOML {
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(rp.ConfigContent); err != nil {
			return nil, fmt.Errorf("encode profile config_content: %w", err)
		}
		data = buf.Bytes()
		if agent == "codex" {
			cmd.Args = append(cmd.Args, "--profile", "agent-wt-profile")
		}
	} else {
		data, err = json.MarshalIndent(rp.ConfigContent, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode profile config_content: %w", err)
		}
	}
	if err := snapshotAndWrite(path, data, 0o644); err != nil {
		return nil, err
	}
	return func() error {
		_, err := restoreIfBackedUp(path)
		return err
	}, nil
}

func mergeOpenCodeEnv(cmd *exec.Cmd, content map[string]any) error {
	for i, e := range cmd.Env {
		if !strings.HasPrefix(e, openCodeConfigEnvPrefix) {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(e, openCodeConfigEnvPrefix)), &doc); err != nil {
			return fmt.Errorf("profile: existing OPENCODE_CONFIG_CONTENT is not valid JSON: %w", err)
		}
		for k, v := range content {
			doc[k] = v
		}
		merged, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("profile: re-encode OPENCODE_CONFIG_CONTENT: %w", err)
		}
		cmd.Env[i] = openCodeConfigEnvPrefix + string(merged)
		return nil
	}
	return fmt.Errorf("profile: opencode launch has no OPENCODE_CONFIG_CONTENT to merge into")
}

// backupDir holds pre-write snapshots of files config_content rewrites,
// never a sibling file next to the target (so nothing lands in a repo or
// worktree).
func backupDir() string { return filepath.Join(config.Dir(), "profile-backups") }

func backupKey(target string) string {
	sum := sha256.Sum256([]byte(target))
	return filepath.Join(backupDir(), hex.EncodeToString(sum[:]))
}

func backupPresentPath(target string) string { return backupKey(target) + ".present" }
func backupAbsentPath(target string) string  { return backupKey(target) + ".absent" }

// snapshotAndWrite self-heals any leftover backup for target first (see
// restoreIfBackedUp), then snapshots target's current state (content, or
// "did not exist") before writing data.
func snapshotAndWrite(target string, data []byte, perm os.FileMode) error {
	if _, err := restoreIfBackedUp(target); err != nil {
		return err
	}
	existing, err := os.ReadFile(target)
	switch {
	case os.IsNotExist(err):
		if err := config.WriteFileAtomic(backupAbsentPath(target), []byte{}, 0o600); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if err := config.WriteFileAtomic(backupPresentPath(target), existing, 0o600); err != nil {
			return err
		}
	}
	return config.WriteFileAtomic(target, data, perm)
}

// restoreIfBackedUp restores target from whatever snapshotAndWrite backed
// up, removing the backup marker, and reports whether it found one to
// restore. It is safe to call with no backup present (a no-op) — this is
// the SAME operation for the normal post-launch restore and for
// self-healing an orphaned backup before a fresh write; the two are not
// distinguished by the function, only by when the caller invokes it.
func restoreIfBackedUp(target string) (restored bool, err error) {
	if _, err := os.Stat(backupAbsentPath(target)); err == nil {
		if rmErr := os.Remove(target); rmErr != nil && !os.IsNotExist(rmErr) {
			return false, rmErr
		}
		return true, os.Remove(backupAbsentPath(target))
	}
	if data, err := os.ReadFile(backupPresentPath(target)); err == nil {
		if werr := config.WriteFileAtomic(target, data, 0o644); werr != nil {
			return false, werr
		}
		return true, os.Remove(backupPresentPath(target))
	}
	return false, nil
}
