import http from "node:http";

const port = Number(process.env.PORT || 3000);
const upstream = process.env.UPSTREAM_MCP_URL;
const upstreamKey = process.env.UPSTREAM_MCP_KEY;
const routeKey = process.env.PRIVATE_ROUTE_KEY;

if (!upstream || !upstreamKey || !routeKey) throw new Error("Missing proxy configuration");

http.createServer(async (req, res) => {
  const expected = `/mcp/${routeKey}`;
  const pathname = new URL(req.url, "http://localhost").pathname.replace(/\/$/, "");
  if (req.url === "/health") {
    res.writeHead(200, { "content-type": "application/json" });
    return res.end('{"ok":true}');
  }
  if (pathname !== expected) {
    console.log(JSON.stringify({ method: req.method, pathname, status: 404 }));
    res.writeHead(404, { "content-type": "text/plain" });
    return res.end("not found");
  }
  try {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const options = {
      method: req.method,
      headers: {
        "authorization": `Bearer ${upstreamKey}`,
        "content-type": req.headers["content-type"] || "application/json",
        "accept": req.headers.accept || "application/json, text/event-stream",
        ...(req.headers["mcp-protocol-version"] ? { "mcp-protocol-version": req.headers["mcp-protocol-version"] } : {})
      },
      body: ["GET", "HEAD"].includes(req.method) ? undefined : Buffer.concat(chunks)
    };
    let response;
    for (let attempt = 0; attempt < 6; attempt += 1) {
      response = await fetch(upstream, options);
      if (response.status !== 502 || attempt === 5) break;
      await new Promise(resolve => setTimeout(resolve, 5000));
    }
    const headers = {};
    for (const name of ["content-type", "mcp-session-id"]) {
      const value = response.headers.get(name); if (value) headers[name] = value;
    }
    res.writeHead(response.status, headers);
    res.end(Buffer.from(await response.arrayBuffer()));
    console.log(JSON.stringify({ method: req.method, pathname, upstreamStatus: response.status }));
  } catch (error) {
    console.error(JSON.stringify({ method: req.method, pathname, error: error.message }));
    res.writeHead(502, { "content-type": "application/json" });
    res.end(JSON.stringify({ error: "upstream unavailable" }));
  }
}).listen(port, "0.0.0.0");
