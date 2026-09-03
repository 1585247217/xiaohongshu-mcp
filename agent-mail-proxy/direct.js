import http from "node:http";
import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { z } from "zod";

const port = Number(process.env.PORT || 3000);
const routeKey = process.env.PRIVATE_ROUTE_KEY;
const recipient = process.env.ALLOWED_RECIPIENT || "1585247217@qq.com";
const cliPath = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "node_modules/.bin/agently-cli");
const cliEnv = () => ({ ...process.env, AGENTLY_CLI_CONFIG_DIR: process.env.AGENTLY_CLI_CONFIG_DIR || "/tmp/agently" });
let login = { child: null, url: null, done: false, error: null };

function runCli(args, timeoutMs = 30000) {
  return new Promise((resolve, reject) => {
    const child = spawn(cliPath, args, { env: cliEnv(), stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "", stderr = "";
    const timer = setTimeout(() => { child.kill("SIGTERM"); reject(new Error("Agent Mail CLI timeout")); }, timeoutMs);
    child.stdout.on("data", c => { stdout += c; }); child.stderr.on("data", c => { stderr += c; });
    child.on("error", reject);
    child.on("close", code => { clearTimeout(timer); if (code) return reject(new Error(stderr || stdout)); try { resolve(JSON.parse(stdout)); } catch { resolve({ ok: true, text: stdout }); } });
  });
}
const output = value => ({ content: [{ type: "text", text: JSON.stringify(value, null, 2) }] });
function makeServer() {
  const server = new McpServer({ name: "Agent Mail", version: "1.0.0" });
  server.registerTool("get_mailbox", { description: "Get the authorized Agent Mail mailbox.", inputSchema: {}, annotations: { readOnlyHint: true } }, async () => output(await runCli(["+me"])));
  server.registerTool("list_emails", { description: "List recent Agent Mail messages.", inputSchema: { limit: z.number().int().min(1).max(50).default(10) }, annotations: { readOnlyHint: true } }, async ({ limit }) => output(await runCli(["message", "+list", "--limit", String(limit)])));
  server.registerTool("read_email", { description: "Read an Agent Mail message by id.", inputSchema: { id: z.string().min(1) }, annotations: { readOnlyHint: true } }, async ({ id }) => output(await runCli(["message", "+read", "--id", id])));
  server.registerTool("send_email", { description: `Send email to ${recipient} only.`, inputSchema: { to: z.string().email().default(recipient), subject: z.string().min(1), body: z.string().min(1) } }, async ({ to, subject, body }) => {
    if (to.toLowerCase() !== recipient.toLowerCase()) throw new Error("Recipient is not allowed");
    const args = ["message", "+send", "--to", to, "--subject", subject, "--body", body];
    const preview = await runCli(args); const token = preview?.data?.confirmation_token;
    return output(token ? await runCli([...args, "--confirmation-token", token]) : preview);
  });
  return server;
}

http.createServer(async (req, res) => {
  const pathname = new URL(req.url, "http://localhost").pathname.replace(/\/$/, "");
  if (pathname === "/health") { res.writeHead(200, { "content-type": "application/json" }); return res.end('{"ok":true}'); }
  if (pathname === `/auth/${routeKey}`) {
    if (login.child && !login.done && login.url) { res.writeHead(302, { location: login.url }); return res.end(); }
    if (!login.child || login.done) {
      login = { child: null, url: null, done: false, error: null };
      const child = spawn(cliPath, ["auth", "login"], { env: cliEnv(), stdio: ["ignore", "pipe", "pipe"] }); login.child = child;
      const inspect = c => { const m = String(c).match(/https?:\/\/[^\s]+/); if (m && !login.url) login.url = m[0]; };
      child.stdout.on("data", inspect); child.stderr.on("data", inspect); child.on("close", code => { login.done = true; if (code) login.error = `login exited ${code}`; });
    }
    const wait = setInterval(() => { if (login.url || login.done) { clearInterval(wait); if (login.url) { res.writeHead(302, { location: login.url }); res.end(); } else { res.writeHead(500); res.end(login.error || "No authorization URL"); } } }, 100);
    return setTimeout(() => { clearInterval(wait); if (!res.headersSent) { res.writeHead(504); res.end("Authorization URL timeout"); } }, 15000);
  }
  if (pathname === `/status/${routeKey}`) { try { const value = await runCli(["auth", "status"]); res.writeHead(200, { "content-type": "application/json" }); return res.end(JSON.stringify(value)); } catch (e) { res.writeHead(401); return res.end(JSON.stringify({ ok: false, error: e.message })); } }
  if (pathname !== `/mcp/${routeKey}`) { res.writeHead(404); return res.end("not found"); }
  const chunks=[]; for await (const chunk of req) chunks.push(chunk);
  const server = makeServer(); const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined, enableJsonResponse: true });
  res.on("close", () => { transport.close(); server.close(); }); await server.connect(transport); await transport.handleRequest(req, res, Buffer.concat(chunks));
}).listen(port, "0.0.0.0");
