from __future__ import annotations

import contextlib
import http.client
import importlib.util
import io
import json
import math
import os
import subprocess
import sys
import tempfile
import unittest
import urllib.error
from pathlib import Path
from unittest.mock import patch

TOOL = Path(__file__).resolve().parents[1] / "decisionbench.py"
SPEC = importlib.util.spec_from_file_location("decisionbench", TOOL)
assert SPEC is not None and SPEC.loader is not None
bench = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = bench
SPEC.loader.exec_module(bench)


class DecisionBenchTests(unittest.TestCase):
    def setUp(self) -> None:
        self.cases = bench.load_suite(bench.SUITE)
        self.case = self.cases[0]
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.addCleanup(self.temp.cleanup)

    def args(self, *extra: str):
        return bench.parser().parse_args(list(extra))

    def result(self, model="jev"):
        return json.loads(bench.mock_payload(self.case, model))

    def run_quiet(self, args, transport=bench.transmit):
        with contextlib.redirect_stdout(io.StringIO()):
            return bench.run(args, transport)

    def assert_code(self, code, function, *args):
        with self.assertRaisesRegex(bench.BenchError, code):
            function(*args)

    def test_same_common_body_and_schedule(self) -> None:
        for model in bench.MODELS:
            request = json.loads(bench.request_body(self.case, model))
            request.pop("model")
            self.assertEqual(request, self.case.common())
        jobs = bench.schedule([self.case], list(bench.MODELS), 3, 0)
        self.assertEqual(
            [j[1] for j in jobs],
            [
                "jev",
                "clef",
                "clef-flash",
                "clef",
                "clef-flash",
                "jev",
                "clef-flash",
                "jev",
                "clef",
            ],
        )

    def test_fixture_not_constant_position(self) -> None:
        winners = {max(c.grades, key=c.grades.get) for c in self.cases[:10]}
        self.assertGreaterEqual(len(winners), 4)
        long_cases = [c for c in self.cases if c.identifier.startswith("long-")]
        self.assertEqual(len(long_cases), 3)
        self.assertEqual(
            len({json.dumps(c.questions, sort_keys=True) for c in long_cases}), 1
        )
        self.assertEqual(
            len({json.dumps(c.grades, sort_keys=True) for c in long_cases}), 1
        )
        for index, case in enumerate(long_cases):
            text = case.state["logs"]["kernel"]
            position = text.index("Out of memory") / len(text)
            self.assertTrue(
                [position < 0.05, 0.4 < position < 0.6, position > 0.95][index]
            )

    def test_preview_never_reads_credentials_or_calls_transport(self) -> None:
        with patch.object(
            bench, "credentials", side_effect=AssertionError("credential read")
        ):
            self.assertEqual(
                self.run_quiet(self.args(), lambda *args: self.fail("network")), 0
            )
        self.assertEqual(list(self.root.iterdir()), [])

    def test_missing_budget_and_credentials_before_output(self) -> None:
        output = self.root / "out"
        args = self.args(
            "--live", "--output", str(output), "--region", "jp", "--plan", "cf-free"
        )
        self.assert_code("EXPLICIT_REQUEST_BUDGET_REQUIRED", self.run_quiet, args)
        args.max_requests = 1000
        with patch.dict(os.environ, {}, clear=True):
            self.assert_code(
                "CREDENTIALS_MISSING",
                self.run_quiet,
                args,
                lambda *a: self.fail("network"),
            )
        self.assertFalse(output.exists())

    def test_credentials_and_account_format(self) -> None:
        for key, value in [
            ("CLOUDFLARE_AUTH_TOKEN", "secret\n"),
            ("CLOUDFLARE_ACCOUNT_ID", "../other"),
        ]:
            env = {
                "JEV_API_KEY": "test-je v",
                "CLOUDFLARE_AUTH_TOKEN": "test-only",
                "CLOUDFLARE_ACCOUNT_ID": "a" * 32,
            }
            env["JEV_API_KEY"] = "test-jev"
            env[key] = value
            with self.assertRaises(bench.BenchError):
                bench.credentials(list(bench.MODELS), env)
        env = {"JEV_API_KEY": "test-jev"}
        self.assertEqual(bench.credentials(["jev"], env)["jev"].token, "test-jev")
        self.assert_code(
            "ACCOUNT_ID_FORMAT",
            bench.endpoint,
            "clef",
            bench.Credentials("test", "../outside"),
        )

    def test_cloudflare_envelope_and_model(self) -> None:
        for model in bench.MODELS:
            parsed = bench.parse_response(
                bench.mock_payload(self.case, model), self.case, model
            )
            self.assertEqual(parsed["quality"]["top1"], True)
        raw = self.result("clef")
        raw["success"] = False
        self.assert_code(
            "CLOUDFLARE_ENVELOPE",
            bench.parse_response,
            bench.encode(raw),
            self.case,
            "clef",
        )
        raw = self.result("clef")
        raw["result"]["model"] = "clef-flash"
        self.assert_code(
            "MODEL_MISMATCH", bench.parse_response, bench.encode(raw), self.case, "clef"
        )
        self.assert_code(
            "CLOUDFLARE_ENVELOPE",
            bench.parse_response,
            bench.encode(self.result()),
            self.case,
            "clef",
        )

    def test_score_contract_boundary_and_mutations(self) -> None:
        key = next(iter(self.case.questions))
        for field, value in [
            ("score", -1),
            ("score", 3.01),
            ("score", True),
            ("score", "3"),
            ("score", None),
            ("score", 10**400),
            ("confidence", -1),
            ("confidence", 1.1),
            ("confidence", False),
        ]:
            with self.subTest(field=field, value=str(value)[:10]):
                raw = self.result()
                raw["answers"][key][field] = value
                self.assert_code(
                    "ANSWER_RANGE",
                    bench.parse_response,
                    bench.encode(raw),
                    self.case,
                    "jev",
                )
        raw = self.result()
        del raw["answers"][key]
        self.assert_code(
            "ANSWER_IDS", bench.parse_response, bench.encode(raw), self.case, "jev"
        )
        raw = self.result()
        raw["answers"][key]["type"] = "choice"
        self.assert_code(
            "ANSWER_TYPE", bench.parse_response, bench.encode(raw), self.case, "jev"
        )
        raw = self.result()
        del raw["answers"][key]["confidence"]
        self.assert_code(
            "ANSWER_RANGE", bench.parse_response, bench.encode(raw), self.case, "jev"
        )

    def test_json_nonfinite_duplicate_and_usage(self) -> None:
        for payload in [b'{"a":NaN}', b'{"a":1,"a":2}', b"null", b"not json"]:
            with self.assertRaises(bench.BenchError):
                bench.parse_response(payload, self.case, "jev")
        for tokens in [True, -1, 1.5, "50", None, 10**400]:
            raw = self.result()
            raw["usage"]["input_tokens"] = tokens
            self.assert_code(
                "USAGE_SCHEMA",
                bench.parse_response,
                bench.encode(raw),
                self.case,
                "jev",
            )

    def test_quality_wrong_ranking_and_ties(self) -> None:
        grades = {"a": 0, "b": 3, "c": 2, "d": 0}
        scores = {key: {"score": 1} for key in grades}
        q = bench.quality(grades, list(grades), scores)
        self.assertFalse(q["top1"])
        self.assertTrue(q["top3"])
        self.assertEqual(q["top_tie_count"], 4)
        ideal = 7 + 3 / math.log2(3)
        observed = 7 / math.log2(3) + 3 / math.log2(4)
        self.assertAlmostEqual(q["ndcg"], observed / ideal)
        self.assertIsNone(bench.quality(None, list(grades), scores))

    def test_summary_excludes_failures_and_warmups(self) -> None:
        good = {
            "warmup": False,
            "status": "PASS",
            "elapsed_seconds": 1,
            "quality": {"top1": True, "top3": True, "ndcg": 1, "top_tie_count": 1},
            "input_tokens": 100,
            "estimated_usd": 0.001,
        }
        rows = [
            good,
            {**good, "elapsed_seconds": 3},
            {**good, "warmup": True, "elapsed_seconds": 99},
            {"warmup": False, "status": "FAIL"},
        ]
        stat = bench.statistics_for(rows)
        self.assertEqual(
            (stat["attempts"], stat["successes"], stat["failures"]), (3, 2, 1)
        )
        self.assertEqual((stat["p50_seconds"], stat["p95_seconds"]), (2, 3))
        self.assertEqual(stat["input_tokens"], 200)
        self.assertEqual(stat["estimated_usd"], 0.002)
        self.assertEqual(stat["warmup_attempts"], 1)

    def test_run_failure_stops_and_preserves_partial_record(self) -> None:
        output = self.root / "out"
        args = self.args(
            "--live",
            "--models",
            "jev",
            "--max-requests",
            "100",
            "--region",
            "jp",
            "--plan",
            "test",
            "--output",
            str(output),
            "--repeats",
            "1",
        )
        calls = []

        def failing(url, body, credential, timeout):
            calls.append(url)
            if len(calls) == 2:
                raise bench.BenchError("HTTP_STATUS:429")
            return bench.mock_payload(self.cases[0], "jev")

        with patch.dict(os.environ, {"JEV_API_KEY": "test-only-secret"}, clear=True):
            self.assertEqual(self.run_quiet(args, failing), 1)
        summary = json.loads((output / "summary.json").read_text())
        self.assertEqual(summary["completed_requests"], 2)
        self.assertEqual(summary["status"], "FAIL")
        self.assertEqual(summary["not_run_requests"], len(self.cases) - 2)
        self.assertEqual(len(calls), 2)
        for path in output.iterdir():
            self.assertNotIn("test-only-secret", path.read_text())
            self.assertNotIn("Out of memory", path.read_text())
        with (
            patch.dict(os.environ, {"JEV_API_KEY": "test-only-secret"}, clear=True),
            self.assertRaises(FileExistsError),
        ):
            self.run_quiet(args, failing)

    def test_output_symlink_rejected(self) -> None:
        target = self.root / "real"
        target.mkdir()
        link = self.root / "link"
        link.symlink_to(target, target_is_directory=True)
        args = self.args("--mock", "--output", str(link / "out"))
        self.assert_code("OUTPUT_SYMLINK", self.run_quiet, args)
        self.assertEqual(list(target.iterdir()), [])

    def test_suite_invalid_and_labels_affect_hash(self) -> None:
        suite = json.loads(bench.SUITE.read_text())
        suite["cases"] = suite["cases"][:1]
        path = self.root / "suite.json"
        first = self.root / "one"
        second = self.root / "two"
        path.write_text(json.dumps(suite))
        self.run_quiet(
            self.args(
                "--mock", "--suite", str(path), "--repeats", "1", "--output", str(first)
            )
        )
        key = next(iter(suite["cases"][0]["grades"]))
        suite["cases"][0]["grades"][key] = 1
        path.write_text(json.dumps(suite))
        self.run_quiet(
            self.args(
                "--mock",
                "--suite",
                str(path),
                "--repeats",
                "1",
                "--output",
                str(second),
            )
        )
        one = json.loads((first / "metadata.json").read_text())
        two = json.loads((second / "metadata.json").read_text())
        self.assertNotEqual(one["suite_sha256"], two["suite_sha256"])
        self.assertEqual(
            one["cases"][0]["common_sha256"], two["cases"][0]["common_sha256"]
        )
        suite["cases"][0]["questions"] = {
            f"q{i}": next(iter(suite["cases"][0]["questions"].values()))
            for i in range(65)
        }
        path.write_text(json.dumps(suite))
        self.assert_code("QUESTION_COUNT", bench.load_suite, path)

    def test_one_and_64_question_limits(self) -> None:
        suite = json.loads(bench.SUITE.read_text())
        entry = suite["cases"][0]
        question = next(iter(entry["questions"].values()))
        suite["cases"] = [entry]
        path = self.root / "boundary.json"
        for count in (1, 64):
            entry["questions"] = {f"log{i}": question for i in range(count)}
            entry["grades"] = {f"log{i}": 3 if i == 0 else 0 for i in range(count)}
            path.write_text(json.dumps(suite))
            case = bench.load_suite(path)[0]
            parsed = bench.parse_response(
                bench.mock_payload(case, "clef"), case, "clef"
            )
            self.assertEqual(len(parsed["scores"]), count)

    def test_mock_no_network_or_credentials(self) -> None:
        with patch.object(
            bench, "credentials", side_effect=AssertionError("credential read")
        ):
            self.assertEqual(
                self.run_quiet(
                    self.args(
                        "--mock",
                        "--repeats",
                        "1",
                        "--output",
                        str(self.root / "mock-no-network"),
                    ),
                    lambda *args: self.fail("network"),
                ),
                0,
            )

    def test_question_order_changes_suite_hash(self) -> None:
        suite = json.loads(bench.SUITE.read_text())
        suite["cases"] = suite["cases"][:1]
        path = self.root / "order.json"
        metadata = []
        for index in range(2):
            path.write_text(json.dumps(suite))
            output = self.root / f"order-{index}"
            self.run_quiet(
                self.args(
                    "--mock",
                    "--suite",
                    str(path),
                    "--repeats",
                    "1",
                    "--output",
                    str(output),
                )
            )
            metadata.append(json.loads((output / "metadata.json").read_text()))
            suite["cases"][0]["questions"] = dict(
                reversed(list(suite["cases"][0]["questions"].items()))
            )
        self.assertNotEqual(metadata[0]["suite_sha256"], metadata[1]["suite_sha256"])
        self.assertEqual(
            metadata[0]["cases"][0]["common_sha256"],
            metadata[1]["cases"][0]["common_sha256"],
        )
        self.assertNotEqual(
            metadata[0]["cases"][0]["question_order"],
            metadata[1]["cases"][0]["question_order"],
        )

    def test_interrupt_writes_failure_summary(self) -> None:
        output = self.root / "interrupt"

        def interrupt(*args):
            raise KeyboardInterrupt()

        args = self.args(
            "--live",
            "--models",
            "jev",
            "--max-requests",
            "100",
            "--region",
            "jp",
            "--plan",
            "test",
            "--output",
            str(output),
        )
        with patch.dict(os.environ, {"JEV_API_KEY": "test-only"}, clear=True):
            self.assertEqual(self.run_quiet(args, interrupt), 130)
        rows = [
            json.loads(line)
            for line in (output / "samples.jsonl").read_text().splitlines()
        ]
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["error_code"], "INTERRUPTED")
        summary = json.loads((output / "summary.json").read_text())
        self.assertEqual(summary["status"], "FAIL")

    def test_transport_fixed_destination_proxy_and_redirect(self) -> None:
        body = bench.request_body(self.case, "jev")

        class FakeResponse:
            status = 200

            def __enter__(self):
                return self

            def __exit__(self, *args):
                pass

            def read1(self, size):
                return b""

        class FakeOpener:
            def open(inner, request, timeout):
                self.assertEqual(
                    request.full_url, "https://api.typesafe.ai/v1/systemone"
                )
                self.assertEqual(request.headers["Authorization"], "Bearer test-only")
                self.assertEqual(timeout, 20)
                return FakeResponse()

        with patch.object(
            bench.urllib.request, "build_opener", return_value=FakeOpener()
        ) as builder:
            self.assertEqual(
                bench.transmit(
                    bench.endpoint("jev", bench.Credentials("test-only")),
                    body,
                    bench.Credentials("test-only"),
                    20,
                ),
                b"",
            )
            handlers = builder.call_args.args
            self.assertEqual(handlers[0].proxies, {})
            self.assertIsNone(
                handlers[1].redirect_request(
                    None, None, 307, "", None, "https://evil.test"
                )
            )
            self.assertEqual(handlers[2]._context.verify_mode, bench.ssl.CERT_REQUIRED)
            self.assertTrue(handlers[2]._context.check_hostname)

    def test_http_errors_and_response_bound(self) -> None:
        for error, code in [
            (
                urllib.error.HTTPError(
                    "https://x.test", 307, "secret body", {}, io.BytesIO(b"secret")
                ),
                "HTTP_STATUS:307",
            ),
            (urllib.error.URLError("secret token"), "NETWORK_ERROR"),
            (http.client.IncompleteRead(b"secret"), "NETWORK_ERROR"),
        ]:
            opener = unittest.mock.Mock()
            opener.open.side_effect = error
            with patch.object(
                bench.urllib.request, "build_opener", return_value=opener
            ):
                self.assert_code(
                    code,
                    bench.transmit,
                    "https://api.typesafe.ai/v1/systemone",
                    b"{}",
                    bench.Credentials("test-only"),
                    20,
                )
        response = unittest.mock.Mock()
        response.read1.side_effect = lambda size: b"a" * size
        self.assert_code("RESPONSE_TOO_LARGE", bench.read_response, response, 20)
        with patch.object(bench.time, "monotonic", side_effect=[0, 21]):
            self.assert_code("RESPONSE_DEADLINE", bench.read_response, response, 20)

    def test_model_change_stops(self) -> None:
        output = self.root / "out"
        count = 0

        def changing(*args):
            nonlocal count
            count += 1
            payload = json.loads(bench.mock_payload(self.cases[count - 1], "jev"))
            payload["model"] = "jev-1" if count == 1 else "jev-2"
            return bench.encode(payload)

        args = self.args(
            "--live",
            "--models",
            "jev",
            "--max-requests",
            "100",
            "--region",
            "jp",
            "--plan",
            "test",
            "--repeats",
            "1",
            "--output",
            str(output),
        )
        with patch.dict(os.environ, {"JEV_API_KEY": "test-only"}, clear=True):
            self.assertEqual(self.run_quiet(args, changing), 1)
        rows = [
            json.loads(line)
            for line in (output / "samples.jsonl").read_text().splitlines()
        ]
        self.assertEqual(rows[-1]["error_code"], "MODEL_CHANGED")

    def test_cli_mock_artifacts_and_preview(self) -> None:
        output = self.root / "mock"
        process = subprocess.run(
            [
                sys.executable,
                str(TOOL),
                "--mock",
                "--repeats",
                "1",
                "--output",
                str(output),
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(process.returncode, 0, process.stderr)
        summary = json.loads((output / "summary.json").read_text())
        self.assertEqual(
            (summary["status"], summary["mode"], summary["completed_requests"]),
            ("PASS", "mock", len(self.cases) * 3),
        )
        self.assertEqual(
            {p.name for p in output.iterdir()},
            {
                "metadata.json",
                "samples.jsonl",
                "summary.json",
                "report.md",
                "report.htm",
            },
        )
        page = (output / "report.htm").read_text()
        self.assertIn('name="viewport"', page)
        self.assertIn("実モデルの性能", page)
        self.assertEqual(os.stat(output).st_mode & 0o777, 0o700)
        self.assertTrue(
            all(os.stat(p).st_mode & 0o777 == 0o600 for p in output.iterdir())
        )
        preview = subprocess.run(
            [sys.executable, str(TOOL)], capture_output=True, text=True, check=False
        )
        self.assertEqual(preview.returncode, 0)
        self.assertEqual(json.loads(preview.stdout)["mode"], "preview")


if __name__ == "__main__":
    unittest.main()
