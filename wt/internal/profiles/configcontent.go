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
	"strconv"
	"strings"
	"syscall"

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
// instead of writing anywhere. claude's worktreePath is resolved to an
// absolute path for the same reason: an outside-a-git-repo launch passes
// the literal relative path "." (cmd/wt/main.go), and backupKey hashes
// the STRING this function returns — two such launches from different
// real directories would otherwise collide on the identical relative
// string ".claude/settings.local.json" and self-heal/restore over each
// other's unrelated file.
func ConfigFileTarget(agent, worktreePath string) (path string, isTOML bool, ok bool, err error) {
	switch agent {
	case "claude":
		abs, absErr := filepath.Abs(worktreePath)
		if absErr != nil {
			return "", false, true, fmt.Errorf("profile: resolve worktree path for claude config_content target: %w", absErr)
		}
		return filepath.Join(abs, ".claude", "settings.local.json"), false, true, nil
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
//
// claude's target (settings.local.json) is NOT a wt-exclusive file — the
// launched Claude Code session itself writes to it during the session
// (e.g. persisting a user-approved "always allow" permission grant) — so
// the write MERGES rp.ConfigContent into whatever is already there
// (mirroring the opencode env-merge below) instead of replacing the whole
// file, and cleanup restores only the leaves config_content itself wrote
// (each value that is not a non-empty object, by its path from the root),
// leaving anything the agent wrote elsewhere alone, a sibling under the same
// object included; a leaf the session changed or deleted after the merge
// keeps the session's state (#401). codex's target is a dedicated,
// wt-owned global file nothing else writes to, so it keeps the simpler
// whole-file snapshot/replace/restore.
//
// rp.ConfigDefaults (claude only) is written after that merge, and only
// each leaf the merged document does not already have: a value the user's
// settings.local.json holds is never written, recorded or reverted (#402).
// A defaults leaf that is written is recorded beside config_content's own,
// so cleanup treats the two alike. Any other agent with ConfigDefaults set
// is refused (Validate already rejects such a profile). It is a no-op only
// when both tables are empty.
func ApplyConfigContent(cmd *exec.Cmd, agent, worktreePath string, rp ResolvedProfile) (cleanup func() error, err error) {
	noop := func() error { return nil }
	if len(rp.ConfigContent) == 0 && len(rp.ConfigDefaults) == 0 {
		return noop, nil
	}
	if len(rp.ConfigDefaults) > 0 && agent != "claude" {
		return nil, fmt.Errorf("profile: agent %q does not take config_defaults", agent)
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

	var buildData func(existing []byte, existed bool) (data []byte, recs []leafRec, err error)
	if isTOML {
		buildData = func(existing []byte, existed bool) ([]byte, []leafRec, error) {
			var buf bytes.Buffer
			if err := toml.NewEncoder(&buf).Encode(rp.ConfigContent); err != nil {
				return nil, nil, fmt.Errorf("encode profile config_content: %w", err)
			}
			return buf.Bytes(), nil, nil // nil recs: codex's target is dedicated, whole-file restore is correct
		}
	} else {
		buildData = func(existing []byte, existed bool) ([]byte, []leafRec, error) {
			doc := map[string]any{}
			if existed {
				if err := json.Unmarshal(existing, &doc); err != nil {
					return nil, nil, fmt.Errorf("profile: existing %s is not valid JSON, refusing to merge config_content: %w", path, err)
				}
				if doc == nil {
					// A whole-file JSON null decodes with no error into a
					// nil map, which mergeInto below would panic on
					// ("assignment to entry in nil map") — crash the launch
					// rather than degrade it. Refuse it like any other
					// content the per-leaf merge cannot act on.
					return nil, nil, fmt.Errorf("profile: existing %s is not a JSON object, refusing to merge config_content", path)
				}
			}
			// Collected before the merge, which changes doc in place.
			// Never nil: a merge that writes no leaf (only {} into existing
			// objects) still gets a marker, so cleanup keeps the session's
			// writes instead of falling back to the whole-file snapshot.
			recs := append([]leafRec{}, collectLeaves(doc, rp.ConfigContent)...)
			mergeInto(doc, rp.ConfigContent)
			// Defaults go after the merge and are judged against it, so a
			// leaf config_content already wrote is not a default's to write.
			// mergeInto put rp.ConfigContent's tables into doc by reference,
			// so setLeaves may write into them too; that is harmless, since
			// rp is a per-launch deep copy (Resolve) dropped after the write.
			defaultRecs := collectAbsentLeaves(doc, rp.ConfigDefaults)
			setLeaves(doc, rp.ConfigDefaults, defaultRecs)
			recs = append(recs, defaultRecs...)
			if existed && len(recs) == 0 && len(rp.ConfigContent) == 0 {
				// Defaults only, and the file already has every one: it
				// stays byte for byte as the user left it.
				return existing, recs, nil
			}
			data, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				return nil, nil, fmt.Errorf("encode profile config_content: %w", err)
			}
			return data, recs, nil
		}
	}

	if err := snapshotAndWrite(path, 0o644, buildData); err != nil {
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
// cmd.Env. It searches from the END of cmd.Env, not the start: BuildLaunchCmd
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
		mergeInto(doc, content)
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

// backupModePath holds target's original permission bits (octal, e.g.
// "0600") alongside its content backup, so restoreIfBackedUpLocked can put
// the file back exactly as it found it instead of guessing a mode.
func backupModePath(target string) string { return backupKey(target) + ".mode" }

// backupKeysPath holds the leaf records (encodeLeafRecs: each leaf's path
// and a hash of the value written, never the value) of what config_content
// merged into target (JSON targets only — see ApplyConfigContent). A marker
// from an older wt holds a list of top-level key names instead, which
// decodeLeafRecs reads as hashless records. Its presence at restore time is
// what selects the per-leaf surgical restore over the whole-file restore; a
// marker that does not decode falls back to the whole-file restore (as an
// older wt does with today's format), and a codex/TOML write never creates
// this file, so codex is unaffected by this mechanism.
func backupKeysPath(target string) string { return backupKey(target) + ".keys" }

// backupOwnerPath records the pid of the wt process that owns target's
// current backup, written right after the backup snapshot and before the
// target itself is rewritten. restoreIfBackedUpLocked uses it to refuse to
// self-heal or restore a backup that still belongs to a different,
// still-running wt process — see withTargetLock's doc for why the lock
// alone isn't enough to prevent that.
func backupOwnerPath(target string) string { return backupKey(target) + ".owner" }

// pidAlive reports whether pid is a live process, mirroring
// internal/refcount.pidAlive's signal-0-probe convention (duplicated
// rather than imported: refcount's contract is scoped to live-session
// model-usage counts, and a cross-package dependency for one liveness
// probe isn't worth it). Package-level var so tests can inject live/dead
// pids deterministically.
var pidAlive = func(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// readBackupOwner reads target's owner marker, reporting ok=false for any
// missing/unreadable/unparseable file — treated as "no known owner",
// which restoreIfBackedUpLocked handles as "not live" (safe to restore),
// matching a backup written by a wt version that predates this file.
func readBackupOwner(target string) (pid int, ok bool) {
	data, err := os.ReadFile(backupOwnerPath(target))
	if err != nil {
		return 0, false
	}
	parsed, perr := strconv.Atoi(strings.TrimSpace(string(data)))
	if perr != nil {
		return 0, false
	}
	return parsed, true
}

// surgicalRestoreJSON reverts each leaf recs names in target's CURRENT
// on-disk content to originalSnapshot's value (revertLeaves), leaving every
// other value in the current content untouched — so content the launched
// agent itself wrote during the session (e.g. a permission grant beside a
// pinned permissions.defaultMode) survives cleanup, and so does a leaf the
// session changed or deleted after the merge. ok is false (caller falls
// back to the whole-file restore) when either originalSnapshot or target's
// current content isn't a JSON object, since a safe per-leaf merge isn't
// possible in that case.
func surgicalRestoreJSON(originalSnapshot []byte, target string, recs []leafRec) (merged []byte, ok bool) {
	var origDoc map[string]any
	// origDoc == nil too: a whole-file null snapshot is not an object this
	// can restore against, so it falls back to the whole-file restore like
	// any other non-object content.
	if err := json.Unmarshal(originalSnapshot, &origDoc); err != nil || origDoc == nil {
		return nil, false
	}
	curDoc, ok := readJSONObject(target)
	if !ok {
		return nil, false
	}
	revertLeaves(curDoc, origDoc, recs)
	data, err := json.MarshalIndent(curDoc, "", "  ")
	if err != nil {
		return nil, false
	}
	return data, true
}

// surgicalDeleteJSON removes the leaves recs names from target's CURRENT
// on-disk content, with the objects the merge created for them once they
// are empty (revertLeaves with no original) — used when config_content
// wrote into a JSON target that did NOT exist before the write, so there
// is no original snapshot to restore values from, only leaves to remove. A
// leaf the session changed after the merge stays. ok is false (caller
// falls back to deleting the whole file, the pre-existing "didn't exist
// before" semantics) when target's current content isn't a JSON object, or
// when removing the leaves leaves nothing behind: an empty object left over
// from a profile-only write (nothing else added) is exactly as pointless a
// file as none at all.
func surgicalDeleteJSON(target string, recs []leafRec) (reduced []byte, ok bool) {
	curDoc, ok := readJSONObject(target)
	if !ok {
		return nil, false
	}
	revertLeaves(curDoc, nil, recs)
	if len(curDoc) == 0 {
		return nil, false
	}
	data, err := json.MarshalIndent(curDoc, "", "  ")
	if err != nil {
		return nil, false
	}
	return data, true
}

// readJSONObject reads target as a JSON object; ok is false when it cannot
// be read or does not hold one (a JSON null included, which decodes to a nil
// map nothing can be set in).
func readJSONObject(target string) (doc map[string]any, ok bool) {
	current, err := os.ReadFile(target)
	if err != nil {
		return nil, false
	}
	if err := json.Unmarshal(current, &doc); err != nil || doc == nil {
		return nil, false
	}
	return doc, true
}

// withTargetLock serializes snapshotAndWrite/restoreIfBackedUp for target
// across processes via an flock on a lock file keyed by target (mirrors
// internal/litellm/configfile.go's WithLock). The lock only serializes the
// brief critical section, though — it says nothing about how long the
// OWNING process then keeps running with that content in place, which is
// what the separate owner-liveness check (backupOwnerPath/pidAlive above)
// guards against.
func withTargetLock(target string, fn func() error) error {
	if err := os.MkdirAll(backupDir(), 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(backupKey(target)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

// snapshotAndWrite self-heals any leftover backup for target first (see
// restoreIfBackedUpLocked) unless it is still owned by a live sibling
// process (in which case it refuses with an error, rather than writing on
// top of a file a different running wt session still relies on), then
// calls buildData to compute the bytes to write (plus the leaf records of
// what it merged in — JSON targets only; nil for a whole-file target like
// codex's) BEFORE writing any backup marker: a buildData
// failure (e.g. existing content is not valid JSON) must leave nothing on
// disk for a later launch's self-heal to mistake for an orphaned crash
// backup and restore over whatever the user has since fixed the file to.
// Only once buildData succeeds does it snapshot target's pre-write state
// (content, permission bits, and this process's pid as owner — or "did
// not exist") and write the new content. The whole sequence runs under
// withTargetLock, so a concurrent snapshotAndWrite or restoreIfBackedUp
// for the same target from another process waits instead of racing it.
// buildData receives target's existing content (nil if it did not exist)
// so a JSON target can merge rather than replace.
func snapshotAndWrite(target string, perm os.FileMode, buildData func(existing []byte, existed bool) (data []byte, recs []leafRec, err error)) error {
	return withTargetLock(target, func() error {
		if _, live, err := restoreIfBackedUpLocked(target); err != nil {
			return err
		} else if live {
			return fmt.Errorf("profile: config_content target %s is already in use by another live wt session", target)
		}
		existing, err := os.ReadFile(target)
		existed := true
		if os.IsNotExist(err) {
			existed = false
		} else if err != nil {
			return err
		}

		data, recs, berr := buildData(existing, existed)
		if berr != nil {
			return berr
		}

		// Nothing to change: leave the file exactly as the user has it.
		// This is the common defaults-only apply, where every leaf the
		// profile defaults is already present (buildData then returns
		// the existing bytes). Rewriting would reformat the JSON, push
		// the mode to perm (0644, widening a 0600 settings file) and
		// touch the mtime for no reason — and a backup marker would
		// make cleanup rewrite the file once more to restore bytes that
		// never changed.
		if existed && bytes.Equal(data, existing) {
			return nil
		}

		if !existed {
			if err := config.WriteFileAtomic(backupAbsentPath(target), []byte{}, 0o600); err != nil {
				return err
			}
		} else {
			info, statErr := os.Stat(target)
			if statErr != nil {
				return statErr
			}
			mode := fmt.Sprintf("%04o", info.Mode().Perm())
			if err := config.WriteFileAtomic(backupModePath(target), []byte(mode), 0o600); err != nil {
				return err
			}
			if err := config.WriteFileAtomic(backupPresentPath(target), existing, 0o600); err != nil {
				return err
			}
		}
		if err := config.WriteFileAtomic(backupOwnerPath(target), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			return err
		}
		// recs is recorded regardless of existed: a JSON target the agent
		// creates fresh during the session (the common case for a worktree
		// with no pre-existing settings.local.json) still needs per-leaf
		// restore, not a wholesale delete of everything the agent wrote —
		// see restoreIfBackedUpLocked's absent branch. A non-nil empty
		// list is recorded too (a merge that wrote no leaf), so cleanup
		// still keeps the session's writes.
		if recs != nil {
			keysJSON, jerr := encodeLeafRecs(recs)
			if jerr != nil {
				return jerr
			}
			if err := config.WriteFileAtomic(backupKeysPath(target), keysJSON, 0o600); err != nil {
				return err
			}
		}
		return config.WriteFileAtomic(target, data, perm)
	})
}

// restoreIfBackedUp restores target from whatever snapshotAndWrite backed
// up, removing the backup marker, and reports whether it found one to
// restore. It is safe to call with no backup present (a no-op) — this is
// the SAME operation for the normal post-launch restore and for
// self-healing an orphaned backup before a fresh write; the two are not
// distinguished by the function, only by when the caller invokes it. Runs
// under withTargetLock so it can't race a concurrent snapshotAndWrite or
// restoreIfBackedUp for the same target in another process. A backup still
// owned by a different, live wt process is left untouched (restored=false,
// err=nil) rather than restored out from under it.
//
// A real filesystem error (permission denied, I/O error) reading either
// marker is returned rather than treated as "no backup" — silently
// swallowing it here would let a caller proceed to overwrite a `.present`
// backup that in fact still holds the true pre-write original, which is
// exactly the data loss self-heal exists to prevent.
func restoreIfBackedUp(target string) (restored bool, err error) {
	err = withTargetLock(target, func() error {
		var lockErr error
		restored, _, lockErr = restoreIfBackedUpLocked(target)
		return lockErr
	})
	return restored, err
}

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

// statExists reports whether path exists, treating a not-exist error as
// (false, nil) and any other stat failure as a real error to propagate.
func statExists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

// restoreIfBackedUpLocked is restoreIfBackedUp's lock-free core. It exists
// separately so snapshotAndWrite's internal self-heal call can run inside
// the single withTargetLock it already holds for its whole snapshot+write
// sequence, instead of nesting a second (deadlocking) lock acquisition.
//
// live=true means a backup exists but its owner marker names a different
// process that is still alive — this deliberately never restores/deletes
// in that case (see backupOwnerPath's doc). The owner check only runs when
// a backup actually exists: a stray/leftover owner marker with no backup
// to protect is meaningless and is cleared instead of blocking anything.
// The owner marker naming THIS SAME process (the common case: a session
// calling restoreIfBackedUp on its own backup at normal cleanup) is never
// treated as live, since a process is always allowed to restore its own
// backup.
func restoreIfBackedUpLocked(target string) (restored bool, live bool, err error) {
	absentPresent, err := statExists(backupAbsentPath(target))
	if err != nil {
		return false, false, err
	}
	presentPresent, err := statExists(backupPresentPath(target))
	if err != nil {
		return false, false, err
	}
	if !absentPresent && !presentPresent {
		_ = os.Remove(backupOwnerPath(target))
		return false, false, nil
	}
	if owner, ok := readBackupOwner(target); ok && owner != os.Getpid() && pidAlive(owner) {
		return false, true, nil
	}

	if absentPresent {
		// The leaf records (recorded even for a target that did not exist
		// before — see snapshotAndWrite) let a JSON target the agent wrote
		// additional content into during the session keep that content:
		// only the profile's own leaves are stripped, not the whole file.
		// Falls back to deleting the whole file (the original "didn't
		// exist before" semantics) when there's no .keys marker or it does
		// not decode, the current content isn't valid JSON, or nothing is
		// left after stripping the profile's leaves.
		if keysRaw, kerr := os.ReadFile(backupKeysPath(target)); kerr == nil {
			if recs, ok := decodeLeafRecs(keysRaw); ok {
				if reduced, ok := surgicalDeleteJSON(target, recs); ok {
					if werr := config.WriteFileAtomic(target, reduced, 0o644); werr != nil {
						return false, false, werr
					}
					_ = os.Remove(backupKeysPath(target))
					_ = os.Remove(backupOwnerPath(target))
					return true, false, os.Remove(backupAbsentPath(target))
				}
			}
		}
		if rmErr := os.Remove(target); rmErr != nil && !os.IsNotExist(rmErr) {
			return false, false, rmErr
		}
		_ = os.Remove(backupKeysPath(target))
		_ = os.Remove(backupOwnerPath(target))
		return true, false, os.Remove(backupAbsentPath(target))
	}

	data, readErr := os.ReadFile(backupPresentPath(target))
	switch {
	case readErr == nil:
		// The stored mode is best-effort: a backup written by an older wt
		// version (or one whose .mode file was lost) has none, and 0o644
		// remains the pre-existing fallback rather than a new failure mode.
		perm := os.FileMode(0o644)
		if modeData, merr := os.ReadFile(backupModePath(target)); merr == nil {
			if parsed, perr := strconv.ParseUint(strings.TrimSpace(string(modeData)), 8, 32); perr == nil {
				perm = os.FileMode(parsed)
			}
		}
		finalData := data
		if keysRaw, kerr := os.ReadFile(backupKeysPath(target)); kerr == nil {
			if recs, ok := decodeLeafRecs(keysRaw); ok {
				if mergedData, ok := surgicalRestoreJSON(data, target, recs); ok {
					finalData = mergedData
				}
			}
		}
		if werr := config.WriteFileAtomic(target, finalData, perm); werr != nil {
			return false, false, werr
		}
		// From here on the target's content is already restored — a
		// cleanup failure below must still report restored=true, mirroring
		// the .absent branch above for the structurally equivalent case.
		if rmErr := os.Remove(backupPresentPath(target)); rmErr != nil {
			return true, false, rmErr
		}
		if rmErr := os.Remove(backupModePath(target)); rmErr != nil && !os.IsNotExist(rmErr) {
			return true, false, rmErr
		}
		if rmErr := os.Remove(backupKeysPath(target)); rmErr != nil && !os.IsNotExist(rmErr) {
			return true, false, rmErr
		}
		_ = os.Remove(backupOwnerPath(target))
		return true, false, nil
	case !os.IsNotExist(readErr):
		return false, false, readErr
	}
	return false, false, nil
}
