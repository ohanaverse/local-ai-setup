<!--
  Extracted from Claude Code v2.1.278
  Source offset: 173934299
  Content hash: 16cea72cd37941b8
  Category: mcp
  Auto-generated — do not edit manually
-->

Re-query the tool lists of connected MCP servers and update the available tools.

Returns one entry per server: the server name, refresh status, current tool count, and which tool names were added or removed relative to what was previously available. Servers that are not currently connected are reported as not_connected (this tool never dials or re-dials connections — it only re-reads the tool list over the existing connection).

Parameters:
- server (optional): The name of a specific MCP server to refresh. If not provided, all connected servers are refreshed.