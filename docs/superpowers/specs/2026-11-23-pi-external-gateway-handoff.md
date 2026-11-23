# Phase 4 handoff: an external LiteLLM gateway for pi (#618)

Status: design pass started 2026-11-23, **nothing designed or built yet**. This note lets the work resume on another machine (the one that can reach the real external gateway). Refs #618, #615, #599. Parent design: [2026-11-22-litellm-aliases-groups-design.md](2026-11-22-litellm-aliases-groups-design.md) (phase 4 is "independent: first answer whether pi can run the credential command itself", line 93).

This repository is public: keep any real gateway name, URL, account or credential out of this file, the issue and every commit. Describe the gateway generically.

## The problem (from #618)

Before #616, `wt -A pi` could reach an external, enterprise-style LiteLLM gateway through a registry provider (a `base_url` plus `secret_ref = "exec:<credential script>"`). On each launch wt ran the script, wrote the provider into pi's `models.json` (with `compat.supportsStore = false` for Bedrock-backed models, which reject the OpenAI `store` parameter) and launched pi against it. #616 deleted that direct-route path on purpose (a session must hold no LiteLLM or provider keys), so with no `wt-model` such a user gets only the plain native launch from #615: pi with whatever is already in pi's own `models.json`, no wt picker, no `-M` for the gateway's models, and a rotating credential that nothing refreshes any more.

Out of scope: `wt-model` fronting such a gateway (it swaps in one static LiteLLM key, which does not fit rotating credentials).

## What is established (read-only research, 2026-11-23)

- **Issue question 1 (can pi run the credential command itself?) very likely yes.** Pi's `models.json` docs say an `apiKey` or `headers` value that starts with `!` runs as a shell command and uses its stdout; `apiKey`/`headers` also support env interpolation and literals; a literal leading `!` is escaped as `$!` in newer docs. A bare `baseUrl` on a built-in provider routes that provider through a proxy. Sources:
  - <https://app.unpkg.com/@earendil-works/pi-coding-agent@1.5.2/files/docs/models.md>
  - <https://app.unpkg.com/@earendil-works/pi-coding-agent@0.83.0/files/docs/models.md>
  - <https://pi.dev/docs/latest/custom-provider>
- **Docs disagree on caching:** a 0.64.0 doc says the command is resolved at request time; a 0.80.9 doc says its output is cached for the process lifetime. A 0.73.1 doc lists `models.json` keys last in credential precedence (after `auth.json` and environment variables); a 1.5.2 doc puts a `models.json` `apiKey` after `auth.json` but before environment variables. `/model` availability checks use configured auth presence and do not execute the command.
- **The installed pi on the dev machine is 1.6.0**, behind the launcher script at `~/.pi/agent/bin/pi` (it execs the managed release under `~/.pi/agent/install/`), with no local docs; 1.6.0 is newer than every doc version found. All of the above must be re-checked against the pi on the corporate machine.
- The old direct-route code is in git history: `git show a5680da2^:wt/internal/agents/pi_models.go` (`syncDirectProviders`, `supportsStoreFalse`, `piCompat`). Issue #6 in `issues.md` (`piCompat` full-pointer replacement in `wt/internal/agents/pi_models.go`) is a related open, deferred item.
- The ruling #618 must make first is the session-split design's anti-goal 3 ([2026-11-15-wt-model-session-split-design.md](2026-11-15-wt-model-session-split-design.md) "Anti-goals": putting the LiteLLM master key, provider keys or any `wt-model` credential on the session side; a credential the agent itself already owns stays wherever it is today). That ruling is open, not settled — see open question 2.

## Open questions (the issue's list, with where the evidence stands)

1. **Is documenting `!command` the whole fix?** Needs a live check on the corporate machine: with pi's own `models.json` pointing at the gateway and `apiKey = "!<credential command>"`, does pi launch, list models and call the gateway, and does a rotated credential get picked up (per request, or only per process)? If yes, the outcome may be a docs page plus a `wt` note, no new code.
2. **If not, or if the picker/`-M` is wanted:** an explicit opt-in external-gateway entry in `config.toml` (base URL plus a credential command, no `wt-model`) that the session resolves at launch and hands to pi. Decide the shape, where it lives (`~/.config/agent-wt/config.toml` is wt-owned; session-side) and how its models show up without a `wt-model` catalog. Whether anti-goal 3 permits this shape is the open ruling: the entry would hold the user's **own** agent credential, which the anti-goal says stays wherever it is today, but the session resolving it at launch is the credential-on-session-side pattern #616 removed on purpose — the pi-runs-the-command shape of question 1 is the one that exemption most clearly covers.
3. **The `supportsStore: false` workaround** for Bedrock-backed gateway models would have to come back for such a provider (or be documented as a `models.json` `compat` entry the user writes).
4. **Which agents:** pi only, or claude, codex and opencode too?

## The question I was about to ask the owner (unanswered)

"Do you actually use, or expect to use, such an external gateway with pi today? And if so, what would you want to happen when you run `wt -A pi` against it?" The answer decides whether this is a docs-only change or a feature. Ask it again on resume (the owner may have it clear already), then follow the brainstorming flow: recon, approaches in a message of their own, a design doc, approval, then a plan.

## How to resume

1. `git fetch && git checkout phase4-pi-gateway` (this branch; it holds only this note on top of `main` at `4e6e6bfa`, phase 3 merged).
2. Read #618, the parent design's phase 4 line, and this note. Re-read `wt/CLAUDE.md` ("Agents", "LiteLLM routing state") and `wt/docs/wt-agents/pi-wt.md`.
3. On the machine with the gateway: run the live check in open question 1 first (read-only first: `pi --version`, look at pi's own `models.json` for an existing provider entry; never print a credential).
4. Then brainstorm and design as above. Standing conventions: TDD, `make pre-pr` before every PR (about 12.5 minutes, run it in the background and read the exit from the log), `Refs #N` and never a closing keyword in a PR body, guides embed no live state, home path placeholder `/Users/user_name`.

## Context that carries over from phase 3

Phase 3 (Claude subscription through the `wt-model` gateway, #597) is merged (#695, #696, #702, #704) and live-checked. Follow-up issues #697 to #701 and #703 are open. The workspace conventions used there (ledgered rulings, strict fail-closed security reviews, scratch-HOME runs for anything that touches config) apply here too.
