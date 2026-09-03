# Agent Mail MCP

Private Streamable HTTP MCP wrapper around the official Tencent QQMail Agently CLI.

Required environment variables: `MCP_API_KEY`, `ALLOWED_RECIPIENT`, and `AGENTLY_CLI_CONFIG_DIR`.
It exposes `/health`, `/mcp`, `/auth/start?key=...`, and `/auth/status?key=...`.
The `send_email` tool is restricted to the configured recipient and performs the CLI's required
two-step confirmation only after the MCP caller invokes the tool.
