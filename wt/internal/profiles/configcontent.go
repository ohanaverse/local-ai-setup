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
// project-scoped profile mechanism to use instead — since that file's
// whole point is to be a FIXED global path (never inside a repo or
// worktree), a failure to resolve the home directory is reported via err
// rather than silently falling back to a relative path: ok is still true
// (codex does have a target mechanism), but the caller must fail loudly
// instead of writing anywhere.
func ConfigFileTarget(agent, worktreePath string) (path string, isTOML bool, ok bool, err error) {
	switch agent {
	case "claude":
		return filepath.Join(worktreePath, ".claude", "settings.local.json"), false, true, nil
	case "codex":
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", false, true, fmt.Errorf("profile: resolve home directory for codex config_content target: %w", homeErr)
		}
		return filepath.Join(home, ".codex", "agent-wt-profile.config.toml"), true, true, nil
	default:
		return "", false, false, nil
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
	path, isTOML, ok, targetErr := ConfigFileTarget(agent, worktreePath)
	if targetErr != nil {
		return nil, targetErr
	}
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
	} else {
		data, err = json.MarshalIndent(rp.ConfigContent, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode profile config_content: %w", err)
		}
	}
	if err := snapshotAndWrite(path, data, 0o644); err != nil {
		return nil, err
	}
	// The --profile flag is only appended once the write above has
	// actually succeeded: appending it earlier (even when the write then
	// fails and the caller degrades to an unprofiled launch) would still
	// point codex at a file that is missing or stale, since a failed write
	// leaves no guarantee about the target's contents.
	if agent == "codex" {
		cmd.Args = append(cmd.Args, "--profile", "agent-wt-profile")
	}
	return func() error {
		_, err := restoreIfBackedUp(path)
		return err
	}, nil
}

// mergeOpenCodeEnv merges content into the OPENCODE_CONFIG_CONTENT entry in
// cmd.Env. It searches from the END of cmd.Env, not the start: Command()
// (internal/agents) builds cmd.Env as os.Environ() (the inherited parent
// environment) followed by the driver's own env entries, so if the parent
// process already happens to export OPENCODE_CONFIG_CONTENT (e.g. wt was
// itself launched from inside an opencode session), the FIRST match would
// be that inherited, unrelated value rather than the opencode driver's own
// (later) entry — and since exec.Cmd.Env uses the LAST value for a
// duplicate key, the driver's own untouched entry would then win at exec
// time, silently dropping the merged profile content. The last match is
// always the one that actually takes effect.
func mergeOpenCodeEnv(cmd *exec.Cmd, content map[string]any) error {
	for i := len(cmd.Env) - 1; i >= 0; i-- {
		e := cmd.Env[i]
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
//
// A real filesystem error (permission denied, I/O error) reading either
// marker is returned rather than treated as "no backup" — silently
// swallowing it here would let a caller proceed to overwrite a `.present`
// backup that in fact still holds the true pre-write original, which is
// exactly the data loss self-heal exists to prevent.
// SelfHeal restores target from an orphaned config_content backup, if one
// exists — the exported entry point for callers outside this package
// (cmd/wt's applyProfileForLaunch) that want to self-heal a claude/codex
// config_content target on EVERY launch of that agent, not only the next
// launch that happens to resolve a matching profile for that exact target
// (which is what snapshotAndWrite's own internal self-heal call already
// covers). Safe to call with no backup present (a no-op, restored=false).
func SelfHeal(target string) (restored bool, err error) {
	return restoreIfBackedUp(target)
}

func restoreIfBackedUp(target string) (restored bool, err error) {
	switch _, statErr := os.Stat(backupAbsentPath(target)); {
	case statErr == nil:
		if rmErr := os.Remove(target); rmErr != nil && !os.IsNotExist(rmErr) {
			return false, rmErr
		}
		return true, os.Remove(backupAbsentPath(target))
	case !os.IsNotExist(statErr):
		return false, statErr
	}
	data, readErr := os.ReadFile(backupPresentPath(target))
	switch {
	case readErr == nil:
		if werr := config.WriteFileAtomic(target, data, 0o644); werr != nil {
			return false, werr
		}
		return true, os.Remove(backupPresentPath(target))
	case !os.IsNotExist(readErr):
		return false, readErr
	}
	return false, nil
}
