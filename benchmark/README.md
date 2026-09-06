# Search benchmark

This benchmark compares discovery backends without coupling them to the existing
reader. Every backend receives the same ten queries and is measured for success,
latency, result count, lexical relevance, and duplicate URLs.

## Run

```bash
cp benchmark/backends.example.json benchmark/backends.json
export XHS_MCP_TOKEN='your existing read-only token'
python3 tools/search_benchmark.py --dry-run
python3 tools/search_benchmark.py
```

Results are written as JSON and Markdown under `benchmark/results/`. That directory
is ignored because real responses may contain account-specific search results.

## Add a backend

Backends can be HTTP JSON APIs or local commands that print JSON. Configure the
path to the result array and the title/URL paths inside each result. This keeps
Agent-Reach, OpenCLI, the current `/feeds/search` route, and a public-search adapter
on the same scorecard without forcing them into one runtime.

Do not commit tokens, cookies, benchmark responses, or private result URLs.
