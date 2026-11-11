#!/usr/bin/env python3
"""Exact public download counters, signed ranking, and immutable icon projections."""
import copy
import datetime
import pathlib
import sys
import unittest
from unittest import mock

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import catalog
import downloads


class DownloadTests(unittest.TestCase):
    def test_exact_detail_counter_not_version_or_rounded_summary(self):
        text = '<span>Version downloads</span><h3 title="9">9</h3>'
        text += '<span class="muted"> Total downloads </span>\n<h3 title="12,345">12.3K</h3>'
        self.assertEqual(downloads.parse_downloads(text), 12345)
        self.assertEqual(downloads.parse_downloads('<span>Total downloads</span><h3 title="0">0</h3>'), 0)

    def test_missing_ambiguous_or_rounded_counter_fails(self):
        for text in ('<h3 title="9">9</h3>', '<span>Total downloads</span><h3 title="1.2K">1.2K</h3>',
                     '<span>Total downloads</span><h3 title="-1">-1</h3>',
                     '<span>Total downloads</span><h3 title="1">1</h3>' * 2):
            with self.subTest(text=text), self.assertRaises(ValueError):
                downloads.parse_downloads(text)

    def test_snapshot_covers_all_ids_and_stops_on_failures(self):
        result = downloads.snapshot(["beta", "alpha"], reader=lambda name: {"alpha": 13, "beta": 8}[name])
        self.assertEqual(result["downloads"], {"alpha": 13, "beta": 8})
        catalog.validate_downloads_timestamp(result["downloads_updated_at"])
        def fail(_):
            raise ValueError("synthetic unavailable")
        with self.assertRaisesRegex(ValueError, "unavailable"):
            downloads.snapshot(["alpha", "beta"], reader=fail)
        for invalid in (-1, True, 1.5):
            with self.assertRaises(ValueError):
                downloads.snapshot(["alpha"], reader=lambda _: invalid)

    def test_http_failure_never_becomes_zero(self):
        import urllib.error
        with mock.patch.object(downloads.urllib.request, "urlopen", side_effect=urllib.error.HTTPError(
                "https://github.com/", 404, "synthetic", None, None)):
            with self.assertRaisesRegex(ValueError, "unavailable"):
                downloads.read_downloads("demo")

    def test_sorting_is_numeric_descending_with_deterministic_ties(self):
        body = {"extensions": [{"manifest": {"id": name}} for name in ["a", "c", "b"]]}
        downloads.apply(body, {"downloads_updated_at": "2026-01-02T03:04:05Z", "downloads": {"a": 9, "b": 100, "c": 100}})
        self.assertEqual([entry["manifest"]["id"] for entry in body["extensions"]], ["b", "c", "a"])
        with self.assertRaisesRegex(ValueError, "complete"):
            downloads.apply(body, {"downloads_updated_at": "2026-01-02T03:04:05Z", "downloads": {"a": 1}})

    def test_icon_source_field_is_not_a_runtime_unknown_field(self):
        source = {"id": "demo", "icon": "assets/icon.png", "tags": ["test"]}
        entry = catalog.source_entry(source)
        self.assertNotIn("icon", entry["manifest"])
        self.assertEqual(entry["icon"], "assets/icon.png")
        self.assertEqual(catalog.entry_inventory(entry)["icon"], "assets/icon.png")
        self.assertEqual(source["icon"], "assets/icon.png")
        self.assertEqual(catalog.icon_url("demo", entry["icon"], "1" * 40),
                         "https://raw.githubusercontent.com/nwverse/persona-extensions/" + "1" * 40 + "/extensions/demo/assets/icon.png")
        for path in ("../icon.png", "assets/../icon.png", "/icon.svg", "assets/icon.ico", "assets/a\\b.png"):
            with self.assertRaises(ValueError):
                catalog.source_entry(dict(source, icon=path))
        with self.assertRaises(ValueError):
            catalog.icon_url("demo", "assets/icon.png", "main")

    def test_rfc3339_rejects_invalid_and_naive_timestamps(self):
        for timestamp in ("2026-01-02T03:04:05Z", "2026-01-02T03:04:05.1+08:00"):
            catalog.validate_downloads_timestamp(timestamp)
        for timestamp in (None, "2026-01-02", "2026-01-02T03:04:05", "2026-13-01T00:00:00Z"):
            with self.assertRaises(ValueError):
                catalog.validate_downloads_timestamp(timestamp)


if __name__ == "__main__":
    unittest.main()
