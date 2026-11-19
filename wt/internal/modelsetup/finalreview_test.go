package modelsetup

import (
	"context"
	"encoding/xml"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
)

// plBackupDir is where a rotation's backup of the LiteLLM plist goes: wt-model's
// config home, never ~/Library/LaunchAgents.
const plBackupDir = plConfigHome + "/wt-model/backups"

// TestBuildPlanLocalRefusesAListenAddressWithCredentials pins the local twin:
// a listen address typed with a user:password is refused without echoing it.
func TestBuildPlanLocalRefusesAListenAddressWithCredentials(t *testing.T) {
	p, err := BuildPlan(freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{Listen: "bob:hunter2-PASSWORD@127.0.0.1:8787"}})
	mustRefuse(t, p, err, "user:password")
	if err != nil && strings.Contains(err.Error(), "hunter2-PASSWORD") {
		t.Errorf("refusal quotes the password: %q", err.Error())
	}
}

// TestApplyConfigRefusesAURLWithCredentials pins the apply-time backstop: a
// plan (built by hand) whose config step would write a url with a
// user:password fails that step and never calls Config.Update.
func TestApplyConfigRefusesAURLWithCredentials(t *testing.T) {
	f := apEnv(t)
	p := Plan{Plan: modelmode.Plan{Want: modelmode.Desired{Mode: modelclient.ModeRemote, URL: "https://u:pw-SECRET@gpu.example:8787", TokenFile: plRemoteTok}, Steps: []Step{{Kind: StepConfig, Path: plCfgPath}}}}
	r, lines := apply(t, f, p)
	if r.Failed == nil || r.Failed.Kind != StepConfig || f.called("Config.Update") {
		t.Fatalf("Failed = %+v, Config.Update called %t", r.Failed, f.called("Config.Update"))
	}
	noSecrets(t, f, lines, r, "pw-SECRET")
}

// TestApplyRotateKeepsTheBackupOutOfLaunchAgents pins that a rotation's backup
// (same Label, old key) never lands in ~/Library/LaunchAgents, where launchd
// or Background Task Management could pick it up: it goes to wt-model's
// backups directory, and the plan says where.
func TestApplyRotateKeepsTheBackupOutOfLaunchAgents(t *testing.T) {
	f := rotEnv(t)
	p := mustPlan(t, rotateState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}})
	if s, _ := stepOf(p, StepBackup); !strings.Contains(s.Detail, plBackupDir) {
		t.Errorf("backup step does not name %s: %q", plBackupDir, s.Detail)
	}
	r, _ := apply(t, f, p)
	if r.Err != nil {
		t.Fatalf("Apply: %v", r.Err)
	}
	for _, n := range f.fileNames() {
		if strings.HasPrefix(n, plHome+"/Library/LaunchAgents/") && n != plLiteLLM {
			t.Errorf("a file other than the plist sits in LaunchAgents: %s", n)
		}
	}
	b := backupsOf(f, plLiteLLM)
	if len(b) != 1 || filepath.Dir(b[0]) != plBackupDir {
		t.Errorf("backups = %q, want one in %s", b, plBackupDir)
	}
}

// TestApplyRotateRefusalLeavesNoBackup pins that an edit the plist refuses (a
// repeated master key) is found before the backup, so a refused rotation
// leaves no stray copy of the old key, and the Recovery says the plist and the
// pair were not touched and no backup was kept.
func TestApplyRotateRefusalLeavesNoBackup(t *testing.T) {
	f := rotEnv(t)
	f.files[plLiteLLM] = []byte(envPlist("", "\t\t<key>LITELLM_MASTER_KEY</key>\n\t\t<string>"+apOldMaster+"</string>\n"+
		"\t\t<key>LITELLM_MASTER_KEY</key>\n\t\t<string>"+apOldMaster+"</string>\n"))
	r, lines := apply(t, f, mustPlan(t, rotateState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}}))
	if r.Failed == nil || r.Failed.Kind != StepBackup {
		t.Fatalf("Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	if f.called("FS.Backup") || len(backupsOf(f, plLiteLLM)) != 0 {
		t.Errorf("a refused rotation made a backup")
	}
	// An earlier token step of the same plan may have written server.token, so
	// the Recovery says what was not touched, never "nothing was changed".
	if !strings.Contains(r.Recovery, "pair were not touched") || !strings.Contains(r.Recovery, "no backup was kept") ||
		strings.Contains(r.Recovery, "nothing was changed") {
		t.Errorf("Recovery = %q", r.Recovery)
	}
	noSecrets(t, f, lines, r, apOldMaster)
}

// TestApplyRotateRechecksTheSaltGuardAtApplyTime pins that the DATABASE_URL
// without LITELLM_SALT_KEY refusal is checked again on the file as it is when
// applied (it may have changed since the plan), before the backup and before
// any write.
func TestApplyRotateRechecksTheSaltGuardAtApplyTime(t *testing.T) {
	f := rotEnv(t)
	f.files[plLiteLLM] = []byte(envPlist("", "\t\t<key>LITELLM_MASTER_KEY</key>\n\t\t<string>"+apOldMaster+"</string>\n"+
		"\t\t<key>DATABASE_URL</key>\n\t\t<string>postgresql://u:db-SECRET@localhost/litellm</string>\n"))
	before := append([]byte(nil), f.files[plLiteLLM]...)
	r, lines := apply(t, f, mustPlan(t, rotateState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}}))
	if r.Failed == nil || r.Failed.Kind != StepBackup || !strings.Contains(r.Err.Error(), "LITELLM_SALT_KEY") {
		t.Fatalf("Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	if f.called("FS.Backup") || f.called("FS.WriteAtomic") || string(f.files[plLiteLLM]) != string(before) {
		t.Errorf("the guard did not stop before the backup and the write")
	}
	noSecrets(t, f, lines, r, apOldMaster, "db-SECRET")
}

// TestApplyAgentPlistStepsRecheckTheFileAtApplyTime pins one rule for the
// write and the removal of wt's LaunchAgent plist: the file is read again
// when applied and must be absent or managed by wt (marker and label); a
// foreign file that appeared since the plan is neither overwritten nor
// removed.
func TestApplyAgentPlistStepsRecheckTheFileAtApplyTime(t *testing.T) {
	foreign := []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>Label</key><string>local.wt-model.serve</string></dict></plist>`)

	f := apEnv(t)
	f.files[plAgentPath] = foreign
	r, _ := apply(t, f, mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}))
	if r.Failed == nil || r.Failed.Kind != StepPlist || string(f.files[plAgentPath]) != string(foreign) {
		t.Errorf("write over a foreign plist: Failed = %+v, Err = %v", r.Failed, r.Err)
	}

	f = apEnv(t)
	f.files[plAgentPath] = foreign
	r, _ = apply(t, f, mustPlan(t, baseState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeNative}, AdminWant: AdminWant{RemoveAgent: true}}))
	if r.Failed == nil || r.Failed.Kind != StepRemovePlist || f.called("FS.Remove") {
		t.Errorf("removal of a foreign plist: Failed = %+v, Err = %v", r.Failed, r.Err)
	}

	f = apEnv(t)
	f.files[plAgentPath] = RenderAgentPlist(plSpec())
	if r, _ = apply(t, f, mustPlan(t, baseState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeNative}, AdminWant: AdminWant{RemoveAgent: true}})); r.Err != nil {
		t.Errorf("removal of wt's own plist failed: %v", r.Err)
	}
	if _, ok := f.files[plAgentPath]; ok {
		t.Errorf("wt's own plist was not removed")
	}
}

// TestApplyPairNeverQuotesAPlistDecoderError pins that an XML decoder error
// from reading the LiteLLM plist (whose text can quote a fragment of a file
// that holds secrets) becomes a fixed message.
func TestApplyPairNeverQuotesAPlistDecoderError(t *testing.T) {
	f := apEnv(t)
	f.plistErr = &xml.SyntaxError{Msg: "invalid character entity &sk-LEAK-0123456789abcdef;", Line: 3}
	r, lines := apply(t, f, mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}))
	if r.Failed == nil || r.Failed.Kind != StepPair {
		t.Fatalf("Failed = %+v, Err = %v", r.Failed, r.Err)
	}
	noSecrets(t, f, lines, r, "sk-LEAK")
}

// TestBuildPlanRefusesAConfigFileItCannotWrite pins that a decode error in
// config.toml stops the plan before any step (the config step is last, so it
// would otherwise fail after the token, plist and launchd steps ran): a type
// error inside [model-server] always, one elsewhere in the file whenever the
// plan writes config.toml. A plan that leaves config.toml alone still plans.
func TestBuildPlanRefusesAConfigFileItCannotWrite(t *testing.T) {
	st := freshState()
	st.Config.TableErr = "toml: line 2: incompatible types"
	p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	mustRefuse(t, p, err, plCfgPath, "[model-server]")

	st = freshState()
	st.Config.RestErr = "toml: line 9 (last key \"agents.claude.args\"): incompatible types"
	p, err = BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	mustRefuse(t, p, err, plCfgPath, "agents.claude.args")

	st = rotateState()
	st.Config.RestErr = "toml: line 9: incompatible types"
	mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}})
}

// TestPlansSayARestartCutsRequestsInFlight pins that a plan that restarts a
// running LiteLLM or wt-model (kickstart -k, or a bootout) says the requests
// in flight are cut, unlike `wt litellm restart`, and that a plan that only
// loads a job does not.
func TestPlansSayARestartCutsRequestsInFlight(t *testing.T) {
	rot := mustPlan(t, rotateState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{LiteLLM: LiteLLMRotateKey}})
	if !strings.Contains(rot.Format(), "in flight") {
		t.Errorf("rotate plan does not say requests in flight are cut:\n%s", rot.Format())
	}
	fresh := mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	if strings.Contains(fresh.Format(), "in flight") {
		t.Errorf("a plan that only loads a job speaks of cut requests:\n%s", fresh.Format())
	}
}

// TestRealFSRefusesPathsOutsideTheTempDirUnderTest pins that this package's
// own Remove seam never changes a file outside os.TempDir() from a test
// binary, so a test that reaches DefaultEnv cannot touch the developer's
// LaunchAgents or config home. (modelmode holds the same rule for its
// WriteAtomic and Chmod seams under the same test name.)
func TestRealFSRefusesPathsOutsideTheTempDirUnderTest(t *testing.T) {
	outside := "/nonexistent-wt-test-root/Library/LaunchAgents/x.plist"
	fsys := realFS{}
	for name, err := range map[string]error{
		"Remove": fsys.Remove(outside),
	} {
		if err == nil || !strings.Contains(err.Error(), "test binary") {
			t.Errorf("%s outside the temp dir: err = %v, want a test-binary refusal", name, err)
		}
	}
}

// TestObserveCarriesADecodeErrorElsewhereIntoState pins the wiring from the
// tolerant read to the plan: a decode error outside [model-server] reaches
// State.Config.RestErr, which BuildPlan refuses on before any step.
func TestObserveCarriesADecodeErrorElsewhereIntoState(t *testing.T) {
	f := obsConfigured(t)
	f.configFile.RestErr = errors.New("toml: line 2 (last key \"agents.name\"): incompatible types")
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Config.RestErr == "" {
		t.Fatalf("RestErr not carried into State")
	}
	p, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeRemote, URL: "https://gpu.example:8787", PastedToken: plPasted, SaveUnchecked: true}})
	mustRefuse(t, p, err, "agents.name")
}
