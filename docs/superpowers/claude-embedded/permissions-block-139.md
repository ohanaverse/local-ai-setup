<!--
  Extracted from Claude Code v2.1.278
  Source offset: 199922973
  Content hash: e80b8d5b5ef0d532
  Category: permissions
  Auto-generated — do not edit manually
-->

` tool (load it first with `ToolSearch select:`; auth is handled in-process — do not use curl):

- `{action: "list"}` — list all routines
- `{action: "get", trigger_id: "..."}` — fetch one routine
- `{action: "create", body: {...}}` — create a routine
- `{action: "update", trigger_id: "...", body: {...}}` — partial update
- `{action: "run", trigger_id: "..."}` — run a routine now
- `{action: "list_runs", trigger_id: "..."}` — the routine's recent run sessions, most recently active first
- `{action: "get_run_log", session_id: "..."}` — condensed log of one run (provisioning, tool calls and errors, permission denials, API retries, final result)

To debug a routine that misbehaved, call `list_runs` and then `get_run_log` on the run in question. A fire that was skipped or refused before a session existed (routine paused, a fire cap, a kill switch) or that failed its pre-creation checks (repository access, environment) leaves no run in `list_runs`, and a routine that posts into an existing session adds to that session rather than a new run; when the list is empty or short, check the routine itself with `get` rather than concluding it never fired.

(Note: the API uses `trigger_id` as the parameter name, but the user-facing term is "routine".)

You CANNOT delete routines. If the user asks to delete, direct them to: https://claude.ai/code/routines

## Create body shape

For a recurring schedule:

```json
{
  "name": "AGENT_NAME",
  "cron_expression": "CRON_EXPR",
  "enabled": true,
  "job_config": {
    "ccr": {
      "environment_id": "ENVIRONMENT_ID",
      "session_context": {
        "model": "claude-sonnet-5",
        "sources": [
          {"git_repository": {"url": ""}}
        ],
        "allowed_tools": ["Bash", "Read", "Write", "Edit", "Glob", "Grep"]
      },
      "events": [
        {"data": {
          "uuid": "<lowercase v4 uuid>",
          "session_id": "",
          "type": "user",
          "parent_tool_use_id": null,
          "message": {"content": "PROMPT_HERE", "role": "user"}
        }}
      ]
    }
  }
}
```

For a one-time run, replace `"cron_expression": "CRON_EXPR"` with `"run_once_at": "YYYY-MM-DDTHH:MM:SSZ"` (RFC3339 UTC, must be in the future). Everything else is identical.

Generate a fresh lowercase UUID for `events[].data.uuid` yourself.

Every `events[].data.message` must be the API message shape `{"role": "user", "content": "..."}` — the `role` field is required, never omit it. If you instead write the body in the `session_request` form that list and get return, the same rule applies to `session_request.events[].payload.message`.

## Available MCP Connectors

These are the user's currently connected claude.ai MCP connectors:


When attaching connectors to a routine, use the `connector_uuid` and `name` shown above (the name is already sanitized to only contain letters, numbers, hyphens, and underscores), and the connector's URL. The `name` field in `mcp_connections` must only contain `[a-zA-Z0-9_-]` — dots and spaces are NOT allowed.

**Important:** Infer what services the agent needs from the user's description. For example, if they say "check Datadog and Slack me errors," the agent needs both Datadog and Slack connectors. Cross-reference against the list above and warn if any required service isn't connected. If a needed connector is missing, direct the user to https://claude.ai/customize/connectors to connect it first.

## Environments

Every routine requires an `environment_id` in the job config. This determines where the cloud agent runs. Ask the user which environment to use.


Use the `id` value as the `environment_id` in `job_config.ccr.environment_id`.
${f?