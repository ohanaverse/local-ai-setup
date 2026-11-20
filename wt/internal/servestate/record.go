// Package servestate is the runtime record wt-model serve writes while it
// runs, so a host command can find the live server; it never holds the token.
//
// The record is serve.json in the wt-model home, mode 0600 (the directory is
// made 0700 only when it does not exist yet), and names the server's pid, its bound listen address and the
// path of its token file. A stale record (a server that died without
// removing it) is tolerated: a reader always verifies by pinging the address.
package servestate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
)

// RecordVersion is the record format Write stores and Read accepts.
const RecordVersion = 1

// Record is serve's runtime record.
type Record struct {
	Version   int       `json:"version"`
	PID       int       `json:"pid"`
	Listen    string    `json:"listen"`     // the bound address serve announced
	TokenFile string    `json:"token_file"` // absolute path; never the token
	Started   time.Time `json:"started"`
}

// Store is where the record lives. A Store with an empty Path writes, reads
// and removes nothing.
type Store struct{ Path string }

// Default is serve.json in the wt-model home, or a Store with no path when
// there is no config home.
func Default() Store {
	home := config.ModelHome()
	if home == "" {
		return Store{}
	}
	return Store{Path: filepath.Join(home, "serve.json")}
}

// Write stores r atomically (temp file + rename in the same directory) with
// mode 0600, making the directory 0700 when it does not exist (an existing
// directory keeps its mode). The version written is always RecordVersion.
func (s Store) Write(r Record) error {
	if s.Path == "" {
		return nil
	}
	if err := modelmode.GuardUnderTest(s.Path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	r.Version = RecordVersion
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(s.Path, append(data, '\n'), 0o600)
}

// Read returns the record and true, or false with a nil error when there is
// none. A record that does not decode, or names another version, is an error
// naming the file.
func (s Store) Read() (Record, bool, error) {
	if s.Path == "" {
		return Record{}, false, nil
	}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, false, fmt.Errorf("%s: %w", s.Path, err)
	}
	if r.Version != RecordVersion {
		return Record{}, false, fmt.Errorf("%s: record version %d, want %d", s.Path, r.Version, RecordVersion)
	}
	return r, true, nil
}

// Remove deletes the record only when it names pid: a server removes the
// record it wrote, never one another server wrote over it, and never a file
// it cannot read as a record (that is Read's error).
func (s Store) Remove(pid int) error {
	r, ok, err := s.Read()
	if err != nil {
		return err
	}
	if !ok || r.PID != pid {
		return nil
	}
	if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
