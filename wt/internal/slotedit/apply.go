package slotedit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// Options configures Apply.
type Options struct {
	Drivers  profiles.SlotDrivers
	ModelIDs []string // registry ids a slot of Target may name
	// Target is the profile that must be usable afterwards; "" for a
	// removal, where no profile is checked for usability or ids.
	Target string
	Now    func() time.Time // backup timestamp; time.Now when nil
}

type profKey struct{ agent, name string }

// Apply reads path (a missing file is empty input), runs edit on its bytes,
// checks the result and writes it atomically after copying the old file to
// <path>.bak-<20060102-150405> (a "-2", "-3" ... suffix on a collision). It
// returns the backup path, "" when there was nothing to back up.
//
// Apply refuses, leaving the file untouched and writing no backup, when the
// new text does not parse; Target is not usable; a slot of Target names an id
// outside ModelIDs; a profile still present (matched by agent and name) was
// usable before and is not now; or Unknown gains a key it did not have before.
func Apply(path string, edit func([]byte) ([]byte, error), o Options) (backup string, err error) {
	// Write through a symlink and keep the file's mode: WriteFileAtomic
	// renames over its path, which would replace a link and reset the mode.
	mode := os.FileMode(0o644)
	if resolved, rerr := filepath.EvalSymlinks(path); rerr == nil {
		path = resolved
		if st, serr := os.Stat(path); serr == nil {
			mode = st.Mode().Perm()
		}
	} else if lst, lerr := os.Lstat(path); lerr == nil && lst.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a dangling symlink (its target does not exist); fix or remove the link first", path)
	}
	old, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	newData, err := edit(old)
	if err != nil {
		return "", err
	}
	before, err := profiles.ParseSlots(old)
	if err != nil {
		// The existing file is already broken; nothing to compare against.
		before = profiles.SlotStore{}
	}
	after, err := profiles.ParseSlots(newData)
	if err != nil {
		return "", fmt.Errorf("edited file does not parse: %w", err)
	}
	if err := check(before, after, o); err != nil {
		return "", err
	}
	if existed {
		if backup, err = writeBackup(path, old, mode, o); err != nil {
			return "", err
		}
	}
	if err := config.WriteFileAtomic(path, newData, mode); err != nil {
		if backup != "" {
			_ = os.Remove(backup)
		}
		return "", err
	}
	return backup, nil
}

func check(before, after profiles.SlotStore, o Options) error {
	usableBefore := map[profKey]bool{}
	for _, c := range profiles.ValidateSlots(before, o.Drivers) {
		if c.Usable && c.Err == nil {
			usableBefore[profKey{c.Profile.Agent, c.Profile.Name}] = true
		}
	}
	targetFound := false
	for _, c := range profiles.ValidateSlots(after, o.Drivers) {
		p := c.Profile
		isTarget := o.Target != "" && p.Name == o.Target
		if isTarget {
			targetFound = true
			if !c.Usable || c.Err != nil {
				if c.Err != nil {
					return fmt.Errorf("profile %q would not be usable: %w", p.Name, c.Err)
				}
				return fmt.Errorf("profile %q would not be usable", p.Name)
			}
			for _, slot := range sortedKeys(p.Slots) {
				if id := p.Slots[slot]; !slices.Contains(o.ModelIDs, id) {
					return fmt.Errorf("profile %q slot %s names %q, which is not a registered model", p.Name, slot, id)
				}
			}
		}
		if usableBefore[profKey{p.Agent, p.Name}] && (!c.Usable || c.Err != nil) {
			if c.Err == nil {
				return fmt.Errorf("edit would make profile %q unusable", p.Name)
			}
			return fmt.Errorf("edit would make profile %q unusable: %w", p.Name, c.Err)
		}
	}
	if o.Target != "" && !targetFound {
		return fmt.Errorf("profile %q is not in the edited file", o.Target)
	}
	var added []string
	for _, k := range after.Unknown {
		if !slices.Contains(before.Unknown, k) {
			added = append(added, k)
		}
	}
	if len(added) > 0 {
		return fmt.Errorf("edit adds unknown keys: %v", added)
	}
	return nil
}

// writeBackup copies old to a fresh <path>.bak-<stamp> file, never
// overwriting an existing one.
func writeBackup(path string, old []byte, mode os.FileMode, o Options) (string, error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	base := path + ".bak-" + now().Format("20060102-150405")
	for n := 1; n < 1000; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		werr := f.Chmod(mode) // O_CREATE honors the umask; the backup keeps the file's mode
		if werr == nil {
			_, werr = f.Write(old)
		}
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(name)
			return "", werr
		}
		return name, nil
	}
	return "", fmt.Errorf("no free backup name for %s", base)
}
