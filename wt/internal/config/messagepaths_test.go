package config

import (
	"path/filepath"
	"testing"
)

// TestMessageFilesFollowTheConfigHome pins #700's helpers: with
// XDG_CONFIG_HOME set they name the files under it, with only HOME set the
// files under ~/.config, and with no config home at all the conventional
// ~/.config paths.
func TestMessageFilesFollowTheConfigHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got, want := ProxyHomeFile(), filepath.Join(xdg, "wt-model", "config.toml"); got != want {
		t.Errorf("ProxyHomeFile = %q, want %q", got, want)
	}
	if got, want := ConfigFile(), filepath.Join(xdg, "agent-wt", "config.toml"); got != want {
		t.Errorf("ConfigFile = %q, want %q", got, want)
	}

	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	if got, want := ProxyHomeFile(), filepath.Join(home, ".config", "wt-model", "config.toml"); got != want {
		t.Errorf("with only HOME: ProxyHomeFile = %q, want %q", got, want)
	}
	if got, want := ConfigFile(), filepath.Join(home, ".config", "agent-wt", "config.toml"); got != want {
		t.Errorf("with only HOME: ConfigFile = %q, want %q", got, want)
	}

	t.Setenv("HOME", "")
	if got := ProxyHomeFile(); got != "~/.config/wt-model/config.toml" {
		t.Errorf("no config home: ProxyHomeFile = %q", got)
	}
	if got := ConfigFile(); got != "~/.config/agent-wt/config.toml" {
		t.Errorf("no config home: ConfigFile = %q", got)
	}
}
