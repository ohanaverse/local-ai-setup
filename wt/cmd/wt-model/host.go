package main

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// host is the host's config as wt-model's admin commands read it
// (cmd/wt-session's app.hostConfig, trimmed to what wt-model needs): built
// once in rootCmd and handed to every command that reads the registry.
type host struct {
	// hostCfg is config.toml and registry.toml through config.Load, read on
	// first use and cached; never nil once hostSet (a stand-in when the load
	// failed). hostLoadErr is config.Load's error alone; hostCfgErr is that
	// error or else the validation error.
	hostCfg     *config.Config
	hostLoadErr error
	hostCfgErr  error
	hostSet     bool
}

// hostConfig is the host's config: config.Load (config.toml and the
// registry), read once, on first use, and cached; the error is config.Load's
// alone. The returned config is never nil: an empty default stands in when
// the load failed, so a command that tolerates a load error (the Models
// screen) still has a value to hand on. The cache is unsynchronized: the
// main goroutine only.
func (h *host) hostConfig() (*config.Config, error) {
	h.loadHost()
	return h.hostCfg, h.hostLoadErr
}

// hostConfigErr is hostConfig's load error, or else the loaded config's
// validation error: what a command that needs a valid registry refuses on,
// and what the Models screen shows as its banner.
func (h *host) hostConfigErr() error {
	h.loadHost()
	return h.hostCfgErr
}

// loadHost reads the host's config once.
func (h *host) loadHost() {
	if h.hostSet {
		return
	}
	cfg, err := config.Load()
	if cfg == nil {
		cfg = &config.Config{DefaultTag: "code"}
	}
	verr := err
	if verr == nil {
		verr = cfg.Validate()
	}
	h.hostCfg, h.hostLoadErr, h.hostCfgErr, h.hostSet = cfg, err, verr, true
}

// loadTheme is the active color theme. I/O and TOML parse errors are
// failures; an unknown or empty theme name falls back to the default themes
// returns, so a typo in themes.toml never stops a screen from opening.
func loadTheme() (themes.Theme, error) {
	theme, _, err := themes.Load()
	if err != nil && !themes.IsThemeNameError(err) {
		return theme, err
	}
	return theme, nil
}
