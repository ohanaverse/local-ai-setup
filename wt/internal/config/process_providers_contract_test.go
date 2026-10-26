package config

import (
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

type processContractReady struct {
	Kind         string   `toml:"kind"`
	Command      []string `toml:"command"`
	Path         string   `toml:"path"`
	Equals       string   `toml:"equals"`
	ModelPath    string   `toml:"model_path"`
	EndpointPath string   `toml:"endpoint_path"`
}

type processContractEntry struct {
	Family        string               `toml:"family"`
	Provider      string               `toml:"provider"`
	Binary        string               `toml:"binary"`
	Spawn         []string             `toml:"spawn"`
	Stop          []string             `toml:"stop"`
	Pidfile       string               `toml:"pidfile"`
	Log           string               `toml:"log"`
	PidfileEnv    string               `toml:"pidfile_env"`
	Subcommand    string               `toml:"subcommand"`
	DaemonModule  string               `toml:"daemon_module"`
	PortFlag      string               `toml:"port_flag"`
	ModelFlag     string               `toml:"model_flag"`
	Scope         string               `toml:"scope"`
	DefaultOrigin string               `toml:"default_origin"`
	WtPort        int                  `toml:"wt_port"`
	RestoreAction string               `toml:"restore_action"`
	Ready         processContractReady `toml:"ready"`
}

// TestProcessProvidersContractMatchesTheProviderTable pins
// docs/contracts/process-providers.toml, the mirror of the process specs that
// llmbench reads, to wt's provider table (internal/provider/table.go). Every
// process family must appear exactly once with every field equal, and the file
// may name no family the table lacks, so a change to the Go table that is not
// carried into the file (or the reverse) fails here instead of llmbench
// silently running a server differently from wt.
func TestProcessProvidersContractMatchesTheProviderTable(t *testing.T) {
	data, err := os.ReadFile("../../../docs/contracts/process-providers.toml")
	if err != nil {
		t.Fatalf("read contract file: %v", err)
	}
	var doc struct {
		Process []processContractEntry `toml:"process"`
	}
	if _, err := toml.Decode(string(data), &doc); err != nil {
		t.Fatalf("decode contract file: %v", err)
	}

	byFamily := map[string]processContractEntry{}
	for _, e := range doc.Process {
		if _, dup := byFamily[e.Family]; dup {
			t.Errorf("family %q: listed more than once in the contract file", e.Family)
		}
		byFamily[e.Family] = e
	}

	tableFamilies := provider.ProcessFamilies()
	for name := range byFamily {
		if !slices.Contains(tableFamilies, name) {
			t.Errorf("family %q: in the contract file but has no process spec in the provider table", name)
		}
	}

	for _, fam := range tableFamilies {
		e, ok := byFamily[fam]
		if !ok {
			t.Errorf("family %q: has a process spec in the provider table but no entry in the contract file", fam)
			continue
		}
		spec, _ := provider.ProcessFor(fam)
		facts, _ := provider.FamilyByName(fam)

		str := func(field, got, want string) {
			if got != want {
				t.Errorf("family %q: %s = %q in the contract file, %q in the provider table", fam, field, got, want)
			}
		}
		list := func(field string, got, want []string) {
			if !slices.Equal(got, want) {
				t.Errorf("family %q: %s = %q in the contract file, %q in the provider table", fam, field, got, want)
			}
		}

		ids := provider.FamilyProviderIDs(fam)
		if len(ids) == 0 {
			t.Errorf("family %q: the provider table has no provider id for it", fam)
		} else {
			str("provider", e.Provider, ids[0])
		}
		str("binary", e.Binary, spec.Binary)
		list("spawn", e.Spawn, spec.Spawn)
		list("stop", e.Stop, spec.Stop)
		str("pidfile", e.Pidfile, spec.PidfileDefault)
		str("log", e.Log, spec.LogDefault)
		str("pidfile_env", e.PidfileEnv, spec.PidfileEnv)
		str("subcommand", e.Subcommand, spec.Subcommand)
		str("daemon_module", e.DaemonModule, spec.DaemonModule)
		str("port_flag", e.PortFlag, spec.PortFlag)
		str("model_flag", e.ModelFlag, spec.ModelFlag)
		str("default_origin", e.DefaultOrigin, facts.DefaultOrigin)
		str("wt_port", strconv.Itoa(e.WtPort), strconv.Itoa(facts.WtPort))
		str("restore_action", e.RestoreAction, "stop")

		wantScope := "port"
		if facts.Scope == provider.ScopeMachine {
			wantScope = "machine"
		}
		str("scope", e.Scope, wantScope)

		str("ready.kind", e.Ready.Kind, string(spec.Ready.Kind))
		list("ready.command", e.Ready.Command, spec.Ready.Command)
		str("ready.path", e.Ready.Path, spec.Ready.Path)
		str("ready.equals", e.Ready.Equals, spec.Ready.Equals)
		str("ready.model_path", e.Ready.ModelPath, spec.Ready.ModelPath)
		str("ready.endpoint_path", e.Ready.EndpointPath, spec.Ready.EndpointPath)
	}
}
