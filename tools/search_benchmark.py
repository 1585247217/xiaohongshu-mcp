#!/usr/bin/env python3
"""Compare search backends with the same queries and a common scorecard."""

from __future__ import annotations

import argparse
import json
import os
import statistics
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


@dataclass
class SearchItem:
    title: str
    url: str
    source: str = ""


@dataclass
class RunResult:
    case_id: str
    query: str
    backend: str
    ok: bool
    elapsed_ms: int
    result_count: int
    relevance: float
    unique_url_ratio: float
    error: str
    items: list[SearchItem]


def get_path(value: Any, path: str) -> Any:
    if not path:
        return value
    current = value
    for part in path.split("."):
        if not isinstance(current, dict):
            return None
        current = current.get(part)
    return current


def render(value: Any, query: str) -> Any:
    if isinstance(value, str):
        return value.replace("{query}", query)
    if isinstance(value, list):
        return [render(item, query) for item in value]
    if isinstance(value, dict):
        return {key: render(item, query) for key, item in value.items()}
    return value


def auth_headers(config: dict[str, Any]) -> dict[str, str]:
    headers = {str(k): str(v) for k, v in config.get("headers", {}).items()}
    env_name = config.get("bearer_token_env")
    if env_name:
        token = os.environ.get(env_name, "")
        if not token:
            raise ValueError(f"missing environment variable: {env_name}")
        headers["Authorization"] = f"Bearer {token}"
    return headers


def normalize_items(payload: Any, config: dict[str, Any], backend: str) -> list[SearchItem]:
    raw_items = get_path(payload, config.get("results_path", ""))
    if not isinstance(raw_items, list):
        raise ValueError("configured results_path did not resolve to a list")
    title_path = config.get("title_path", "title")
    url_path = config.get("url_path", "url")
    source_path = config.get("source_path", "")
    items = []
    for raw in raw_items:
        if not isinstance(raw, dict):
            continue
        title = get_path(raw, title_path)
        url = get_path(raw, url_path) if url_path else ""
        if not url and config.get("url_template"):
            values = {
                key: urllib.parse.quote(str(value), safe="")
                for key, value in raw.items()
                if isinstance(value, (str, int, float, bool))
            }
            try:
                url = config["url_template"].format_map(values)
            except KeyError as exc:
                raise ValueError(f"url_template field not found: {exc.args[0]}") from exc
        source = get_path(raw, source_path) if source_path else backend
        items.append(SearchItem(str(title or "").strip(), str(url or "").strip(), str(source or backend)))
    return items


def run_http(config: dict[str, Any], query: str, timeout: float) -> list[SearchItem]:
    method = config.get("method", "GET").upper()
    url = render(config["url"], query)
    body = None
    if method == "GET":
        parts = list(urllib.parse.urlparse(url))
        params = urllib.parse.parse_qsl(parts[4], keep_blank_values=True)
        params.append((config.get("query_param", "q"), query))
        parts[4] = urllib.parse.urlencode(params)
        url = urllib.parse.urlunparse(parts)
    else:
        body = json.dumps(render(config.get("body", {"query": "{query}"}), query), ensure_ascii=False).encode()
    headers = auth_headers(config)
    if body is not None:
        headers.setdefault("Content-Type", "application/json")
    request = urllib.request.Request(url, data=body, headers=headers, method=method)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        payload = json.load(response)
    return normalize_items(payload, config, config["name"])


def run_command(config: dict[str, Any], query: str, timeout: float) -> list[SearchItem]:
    argv = [part.replace("{query}", query) for part in config["argv"]]
    completed = subprocess.run(argv, capture_output=True, text=True, timeout=timeout, check=False)
    if completed.returncode:
        detail = completed.stderr.strip() or completed.stdout.strip()
        raise RuntimeError(f"command exited {completed.returncode}: {detail[:300]}")
    return normalize_items(json.loads(completed.stdout), config, config["name"])


def relevance_score(items: list[SearchItem], terms: list[str]) -> float:
    if not items or not terms:
        return 0.0
    matched = 0
    for item in items[:10]:
        haystack = f"{item.title} {item.url}".casefold()
        if any(term.casefold() in haystack for term in terms):
            matched += 1
    return matched / min(10, len(items))


def unique_url_ratio(items: list[SearchItem]) -> float:
    urls = [item.url for item in items if item.url]
    return len(set(urls)) / len(urls) if urls else 0.0


def execute_case(case: dict[str, Any], backend: dict[str, Any], timeout: float) -> RunResult:
    started = time.monotonic()
    items: list[SearchItem] = []
    error = ""
    try:
        if backend["type"] == "http":
            items = run_http(backend, case["query"], timeout)
        elif backend["type"] == "command":
            items = run_command(backend, case["query"], timeout)
        else:
            raise ValueError(f"unsupported backend type: {backend['type']}")
        ok = True
    except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired, urllib.error.URLError) as exc:
        ok = False
        error = f"{type(exc).__name__}: {exc}"
    return RunResult(
        case_id=case["id"], query=case["query"], backend=backend["name"], ok=ok,
        elapsed_ms=round((time.monotonic() - started) * 1000), result_count=len(items),
        relevance=relevance_score(items, case.get("relevance_terms", [])),
        unique_url_ratio=unique_url_ratio(items), error=error, items=items[:10],
    )


def aggregate(results: list[RunResult]) -> list[dict[str, Any]]:
    rows = []
    for name in sorted({result.backend for result in results}):
        group = [result for result in results if result.backend == name]
        successes = [result for result in group if result.ok]
        rows.append({
            "backend": name,
            "success_rate": len(successes) / len(group),
            "median_elapsed_ms": statistics.median(result.elapsed_ms for result in group),
            "mean_result_count": sum(result.result_count for result in group) / len(group),
            "mean_relevance": sum(result.relevance for result in group) / len(group),
            "mean_unique_url_ratio": sum(result.unique_url_ratio for result in group) / len(group),
        })
    return rows


def markdown_report(report: dict[str, Any]) -> str:
    lines = ["# Search backend benchmark", "", f"Generated: {report['generated_at']}", "",
             "| Backend | Success | Median ms | Results | Relevance | Unique URLs |",
             "| --- | ---: | ---: | ---: | ---: | ---: |"]
    for row in report["summary"]:
        lines.append(f"| {row['backend']} | {row['success_rate']:.0%} | {row['median_elapsed_ms']:.0f} | "
                     f"{row['mean_result_count']:.1f} | {row['mean_relevance']:.0%} | {row['mean_unique_url_ratio']:.0%} |")
    lines.extend(["", "## Runs", "", "| Case | Backend | OK | ms | Count | Relevance | Error |",
                  "| --- | --- | ---: | ---: | ---: | ---: | --- |"])
    for result in report["results"]:
        error = result["error"].replace("|", "\\|")
        lines.append(f"| {result['case_id']} | {result['backend']} | {'yes' if result['ok'] else 'no'} | "
                     f"{result['elapsed_ms']} | {result['result_count']} | {result['relevance']:.0%} | {error} |")
    return "\n".join(lines) + "\n"


def load_json(path: Path) -> Any:
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cases", type=Path, default=Path("benchmark/search_cases.json"))
    parser.add_argument("--config", type=Path, default=Path("benchmark/backends.json"))
    parser.add_argument("--output", type=Path, default=Path("benchmark/results"))
    parser.add_argument("--timeout", type=float, default=65)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args(argv)
    cases = load_json(args.cases)["cases"]
    backends = [backend for backend in load_json(args.config)["backends"] if backend.get("enabled", True)]
    if args.dry_run:
        print(json.dumps({"cases": cases, "backends": backends}, ensure_ascii=False, indent=2))
        return 0
    if not cases or not backends:
        parser.error("at least one case and one enabled backend are required")
    results = [execute_case(case, backend, args.timeout) for case in cases for backend in backends]
    report = {
        "generated_at": datetime.now(timezone.utc).isoformat(), "summary": aggregate(results),
        "results": [{**asdict(result), "items": [asdict(item) for item in result.items]} for result in results],
    }
    args.output.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    json_path = args.output / f"search-benchmark-{stamp}.json"
    md_path = args.output / f"search-benchmark-{stamp}.md"
    json_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    md_path.write_text(markdown_report(report), encoding="utf-8")
    print(md_path)
    return 0 if all(result.ok for result in results) else 2


if __name__ == "__main__":
    sys.exit(main())
