import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
export const cliPath = path.resolve(here, "../node_modules/.bin/agently-cli");
const cliEnv = () => ({ ...process.env, AGENTLY_CLI_CONFIG_DIR: process.env.AGENTLY_CLI_CONFIG_DIR || "/tmp/agently" });

export function runCli(args, { timeoutMs = 30000 } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(cliPath, args, { env: cliEnv(), stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "", stderr = "";
    const timer = setTimeout(() => { child.kill("SIGTERM"); reject(new Error("Agent Mail CLI timed out")); }, timeoutMs);
    child.stdout.on("data", c => { stdout += c; });
    child.stderr.on("data", c => { stderr += c; });
    child.on("error", reject);
    child.on("close", code => {
      clearTimeout(timer);
      if (code !== 0) return reject(new Error(stderr.trim() || stdout.trim() || `CLI exited ${code}`));
      try { resolve(JSON.parse(stdout)); } catch { resolve({ ok: true, text: stdout.trim() }); }
    });
  });
}

export async function sendMail({ to, subject, body }) {
  const args = ["message", "+send", "--to", to, "--subject", subject, "--body", body];
  const preview = await runCli(args);
  const token = preview?.data?.confirmation_token;
  return token ? runCli([...args, "--confirmation-token", token]) : preview;
}
