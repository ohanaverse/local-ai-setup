# claude-session-skills

Skill and system-prompt text recovered from Claude Code **session transcripts**:
the text the harness actually put in front of the model during a session, with
arguments and environment already substituted.

Capture a session with `litellm-session-logs/03_capture_claude_code.sh`, rebuild
it with `build_cc_transcript.py`, then copy the skill or prompt text out of the
transcript. These files are snapshots of one point in time; no tool in this
repo regenerates them.

This is the only skill-extraction set in the repo. An earlier approach scraped
template literals out of the Claude Code binaries (`bin/extract-claude-skills`
into `claude-embedded/`); it was removed because it dropped interpolated
fragments (for example "via the  tool"), omitted whole sections and kept many
near-duplicate blocks.
