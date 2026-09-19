<!--
  Extracted from Claude Code v2.1.278
  Source offset: 193467540
  Content hash: 24f983f885ac608e
  Category: mcp
  Auto-generated — do not edit manually
-->

"Nonessential services" covers three outbound fetches the app runs without: **connector favicons** (the icon proxy), the **artifact-preview** iframe origin, and the **MCP Apps widget** iframe origin (`*.claudemcpcontent.com`). Turning this on blocks all three.

**What you lose when this is on:** connectors show without icons, artifacts do not render in conversations, and connectors that return MCP Apps show the text tool result instead of the widget.

Destinations are listed under Egress Requirements → Nonessential services.