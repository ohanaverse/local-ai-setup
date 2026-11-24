package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// GatewayWrite is what SetGatewayPassthrough did.
type GatewayWrite struct {
	// Changed is false when the table already held the asked value, so
	// nothing was written and no backup made.
	Changed bool
	// Path is the home's config.toml (the target of a symlink, when it is
	// one), Backup the copy of the file it replaced ("" with no change or no
	// file before).
	Path, Backup string
}

// SetGatewayPassthrough is the one writer of [gateway] subscription_passthrough
// in the wt-model home's config.toml (#693): the host switch of the header
// mode, which `wt-model serve` reads once, at start. It changes that one key
// and leaves every other key in the document (the [litellm] table, tls_cert
// and tls_key, anything else) as it was decoded; like SetProxy it re-encodes
// the document, so hand comments go, and it holds the same file lock, keeps
// the file's mode (0644 when new), writes through a symlink and refuses a
// document that does not parse, writing nothing. The file it replaces is
// copied first to backups/config.toml.bak-<date>-<time> beside it (a 0700
// directory, 0600 file). It creates no [litellm] table, so the proxy pair's
// fallback to the legacy table is as it was. A value of the wrong type
// ("true" as a string) is replaced by the boolean asked for. Asking for what
// the table already says writes nothing.
//
// serve must be restarted for the change to take effect.
func SetGatewayPassthrough(on bool) (GatewayWrite, error) {
	home := ProxyHomePath()
	if home == "" {
		return GatewayWrite{}, errors.New("cannot set the gateway switch: no config home (HOME and XDG_CONFIG_HOME are unset)")
	}
	var res GatewayWrite
	err := WithFileLock(home+".lock", func() error {
		target, err := writeTarget(home)
		if err != nil {
			return err
		}
		res.Path = target
		doc, exists, err := decodeProxyHome(target)
		if err != nil {
			return err
		}
		if cur, ok := doc.Gateway[gatewayPassthroughKey].(bool); ok && cur == on {
			return nil
		}
		if doc.Gateway == nil {
			doc.Gateway = map[string]any{}
		}
		doc.Gateway[gatewayPassthroughKey] = on
		var buf bytes.Buffer
		buf.WriteString(proxyHomeHeader)
		if err := toml.NewEncoder(&buf).Encode(&doc); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if exists {
			st, err := os.Stat(target)
			if err != nil {
				return err
			}
			mode = st.Mode().Perm()
			if res.Backup, err = backupHomeConfig(target, time.Now()); err != nil {
				return fmt.Errorf("back up %s before changing it (nothing was written): %w", target, err)
			}
		}
		if err := WriteFileAtomic(target, buf.Bytes(), mode); err != nil {
			return err
		}
		res.Changed = true
		return nil
	})
	if err != nil {
		return GatewayWrite{Path: res.Path}, err
	}
	return res, nil
}

// backupHomeConfig copies the file at src, byte for byte, to
// <wt-model home>/backups/config.toml.bak-<date>-<time> and returns that
// path. The directory is 0700 and the copy 0600 (the file can hold a
// credential helper's file name, and the backup is kept after the original
// changes); a second backup in the same second takes a numbered name, never
// overwriting the first.
func backupHomeConfig(src string, now time.Time) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(ModelHome(), "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	base := filepath.Join(dir, "config.toml.bak-"+now.Format("20060102-150405"))
	path := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			return "", err
		}
		path = fmt.Sprintf("%s-%d", base, i)
	}
	if err := WriteFileAtomic(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
