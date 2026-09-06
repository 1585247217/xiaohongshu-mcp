import importlib.util
import sys
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("search_benchmark.py")
SPEC = importlib.util.spec_from_file_location("search_benchmark", MODULE_PATH)
BENCH = importlib.util.module_from_spec(SPEC)
assert SPEC.loader
sys.modules[SPEC.name] = BENCH
SPEC.loader.exec_module(BENCH)


class SearchBenchmarkTest(unittest.TestCase):
    def test_get_path(self):
        self.assertEqual(BENCH.get_path({"data": {"feeds": [1]}}, "data.feeds"), [1])
        self.assertIsNone(BENCH.get_path({}, "data.feeds"))

    def test_render(self):
        self.assertEqual(BENCH.render({"keyword": "{query}"}, "下雨天"), {"keyword": "下雨天"})

    def test_normalize_items(self):
        payload = {"data": {"feeds": [{"note": {"title": "Hello"}, "url": "https://x/1"}]}}
        config = {"name": "demo", "results_path": "data.feeds", "title_path": "note.title", "url_path": "url"}
        self.assertEqual(BENCH.normalize_items(payload, config, "demo")[0].title, "Hello")

    def test_normalize_items_builds_xhs_url(self):
        payload = {"data": {"feeds": [{"id": "abc", "xsecToken": "a+b=", "noteCard": {"displayTitle": "雨"}}]}}
        config = {
            "name": "xhs", "results_path": "data.feeds", "title_path": "noteCard.displayTitle",
            "url_path": "", "url_template": "https://x/{id}?token={xsecToken}",
        }
        item = BENCH.normalize_items(payload, config, "xhs")[0]
        self.assertEqual(item.url, "https://x/abc?token=a%2Bb%3D")

    def test_scores(self):
        items = [BENCH.SearchItem("AI 主动聊天", "https://x/1"), BENCH.SearchItem("别的内容", "https://x/1")]
        self.assertEqual(BENCH.relevance_score(items, ["主动"]), 0.5)
        self.assertEqual(BENCH.unique_url_ratio(items), 0.5)

    def test_aggregate(self):
        results = [
            BENCH.RunResult("a", "q", "one", True, 20, 2, 0.5, 1.0, "", []),
            BENCH.RunResult("b", "q", "one", False, 40, 0, 0.0, 0.0, "timeout", []),
        ]
        summary = BENCH.aggregate(results)[0]
        self.assertEqual(summary["success_rate"], 0.5)
        self.assertEqual(summary["median_elapsed_ms"], 30)


if __name__ == "__main__":
    unittest.main()
