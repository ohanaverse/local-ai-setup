package modelserver

import (
	"strings"
	"sync/atomic"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionreg"
)

// aliasExpansion maps each alias id to its local target id(s) (#596,
// decision 12): a client session registers the alias id, and the session
// registry counts it as a user of these. Every target that resolves local is
// listed, a malformed or faulty alias's too: over-counting a user only keeps
// a model running, under-counting stops one under a client session.
func aliasExpansion(cfg *config.Config) map[string][]string {
	if cfg == nil || len(cfg.Aliases) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, a := range cfg.Aliases {
		if !servedAliasID(a.ID) {
			continue
		}
		for _, t := range cfg.AliasTargets(a) {
			i := config.IndexModelByID(cfg.Models, t.Model)
			if i < 0 {
				continue
			}
			if loc, err := cfg.ResolveLocation(cfg.Models[i]); err == nil && loc == config.LocationLocal {
				out[a.ID] = append(out[a.ID], t.Model)
			}
		}
	}
	return out
}

// servedAliasID reports whether id can name an alias on the server: only an
// id under config.AliasIDPrefix. The reader keeps a malformed alias whose id
// lacks the prefix (a hand edit), and such an id may be a model's: it must
// never take that model over.
func servedAliasID(id string) bool { return strings.HasPrefix(id, config.AliasIDPrefix) }

// serverAlias is cfg.AliasByID for the server's paths (start, stop, the
// start key): an id without the alias/ prefix is never an alias here.
func serverAlias(cfg *config.Config, id string) (config.Alias, bool) {
	if !servedAliasID(id) {
		return config.Alias{}, false
	}
	return cfg.AliasByID(id)
}

// startKey is the id a start job is keyed by: the local target id for an
// alias with exactly one local target, else id itself (an alias with several
// local targets, #690, is keyed by its own id: it starts them all, so it
// joins no single model's start). A start of alias/x and a start of its one
// target then share one job (modeljobs.SubmitOnce), and Jobs.Pending(target)
// is true while either runs, which keeps auto-stop from forgetting the
// target under the start; pendingStart covers the several-target case.
func startKey(cfg *config.Config, id string) string {
	if cfg == nil {
		return id
	}
	if a, ok := serverAlias(cfg, id); ok {
		if locals := cfg.AliasLocalTargets(a); len(locals) == 1 {
			return locals[0].ID
		}
	}
	return id
}

// pendingStart reports whether a start job is queued or running that covers
// model id: one keyed by the id itself, by an alias's single local target
// (startKey), or by any alias that targets it (a several-target alias is
// keyed by its own id). Auto-stop must neither forget nor stop a model under
// such a start.
func pendingStart(cfg *config.Config, jobs interface{ Pending(string) bool }, id string) bool {
	if jobs.Pending(id) || jobs.Pending(startKey(cfg, id)) {
		return true
	}
	if cfg == nil {
		return false
	}
	for _, aid := range cfg.AliasesTargeting(id) {
		if jobs.Pending(aid) {
			return true
		}
	}
	return false
}

// configTap remembers the last config Deps.Config loaded, so the session
// registry's alias expansion reads the config the current request, job or
// scan already loaded instead of reloading the registry on every session
// read (and never under the registry's lock).
type configTap struct {
	load func() *config.Config
	last atomic.Pointer[config.Config]
}

func (c *configTap) config() *config.Config {
	cfg := c.load()
	c.last.Store(cfg)
	return cfg
}

func (c *configTap) expand() map[string][]string {
	cfg := c.last.Load()
	if cfg == nil {
		cfg = c.config()
	}
	return aliasExpansion(cfg)
}

// tapConfig wires the alias expansion: d's Config is wrapped to remember
// each load, and sessions expands aliases from the last one. The server
// calls it once; a test building an AutoStopper by hand calls it too.
func tapConfig(d Deps, sessions *sessionreg.Registry) Deps {
	tap := &configTap{load: d.Config}
	d.Config = tap.config
	sessions.SetExpand(tap.expand)
	return d
}
