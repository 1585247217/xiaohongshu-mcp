import express from "express";
import { spawn } from "node:child_process";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { z } from "zod";
import { cliPath, runCli, sendMail } from "./cli.js";

const app = express();
app.use(express.json({ limit: "2mb" }));
const apiKey = process.env.MCP_API_KEY;
const recipient = process.env.ALLOWED_RECIPIENT || "1585247217@qq.com";
const oauthPathKey = "qiu-agent-mail-20260903";
const cliEnv = () => ({ ...process.env, AGENTLY_CLI_CONFIG_DIR: process.env.AGENTLY_CLI_CONFIG_DIR || "/tmp/agently" });
let login = { child: null, url: null, done: false, error: null };

const allowed = req => Boolean(apiKey) && (
  req.headers.authorization?.replace(/^Bearer\s+/i, "") === apiKey ||
  req.query.key === apiKey ||
  req.params.key === apiKey
);
const output = value => ({ content: [{ type: "text", text: JSON.stringify(value, null, 2) }] });

function mailServer() {
  const server = new McpServer({ name: "agent-mail", version: "1.0.0" });
  server.registerTool("get_mailbox", { description: "Get the authorized Agent Mail mailbox.", inputSchema: {}, annotations: { readOnlyHint: true } }, async () => output(await runCli(["+me"])));
  server.registerTool("list_emails", { description: "List recent messages.", inputSchema: { limit: z.number().int().min(1).max(50).default(10) }, annotations: { readOnlyHint: true } }, async ({ limit }) => output(await runCli(["message", "+list", "--limit", String(limit)])));
  server.registerTool("read_email", { description: "Read a message by id.", inputSchema: { id: z.string().min(1) }, annotations: { readOnlyHint: true } }, async ({ id }) => output(await runCli(["message", "+read", "--id", id])));
  server.registerTool("send_email", {
    description: `Send mail; restricted to ${recipient}.`,
    inputSchema: { to: z.string().email().default(recipient), subject: z.string().min(1).max(200), body: z.string().min(1).max(100000) },
    annotations: { readOnlyHint: false, destructiveHint: false, idempotentHint: false, openWorldHint: true }
  }, async ({ to, subject, body }) => {
    if (to.toLowerCase() !== recipient.toLowerCase()) throw new Error("Recipient is not allowed");
    return output(await sendMail({ to, subject, body }));
  });
  return server;
}

app.get("/health", (_req, res) => res.json({ ok: true, service: "agent-mail-mcp" }));
app.all("/mcp", async (req, res) => {
  if (!allowed(req)) return res.status(401).json({ error: "unauthorized" });
  const server = mailServer();
  const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined, enableJsonResponse: true });
  res.on("close", () => { transport.close(); server.close(); });
  await server.connect(transport);
  await transport.handleRequest(req, res, req.body);
});

app.get("/auth/start/:key", (req, res) => {
  if (req.params.key !== oauthPathKey) return res.status(401).send("unauthorized");
  if (login.child && !login.done && login.url) return res.redirect(302, login.url);
  if (login.child && !login.done) return res.status(202).send("授权链接正在生成，请几秒后刷新本页。");
  login = { child: null, url: null, done: false, error: null };
  const child = spawn(cliPath, ["auth", "login"], { env: cliEnv(), stdio: ["ignore", "pipe", "pipe"] });
  login.child = child;
  const inspect = chunk => { const m = String(chunk).match(/https?:\/\/[^\s]+/); if (m && !login.url) login.url = m[0]; };
  child.stdout.on("data", inspect); child.stderr.on("data", inspect);
  child.on("error", e => { login.error = e.message; login.done = true; });
  child.on("close", code => { login.done = true; if (code !== 0 && !login.error) login.error = `login exited ${code}`; });
  const poll = setInterval(() => {
    if (login.url || login.done) {
      clearInterval(poll);
      if (login.url) return res.redirect(302, login.url);
      res.status(500).send(login.error || "未能生成授权链接");
    }
  }, 100);
  setTimeout(() => { clearInterval(poll); if (!res.headersSent) res.status(504).json({ error: "authorization URL timeout" }); }, 15000);
});

app.get("/auth/status/:key", async (req, res) => {
  if (req.params.key !== oauthPathKey) return res.status(401).send("unauthorized");
  try { res.json(await runCli(["auth", "status"])); } catch (e) { res.status(401).json({ ok: false, error: e.message }); }
});

app.listen(Number(process.env.PORT || 3000), "0.0.0.0");
