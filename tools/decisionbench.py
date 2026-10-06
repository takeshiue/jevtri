"""Compare decision models on frozen score questions without changing jevtri."""

from __future__ import annotations

import argparse
import hashlib
import html
import http.client
import json
import math
import os
import platform
import re
import ssl
import statistics
import sys
import time
import urllib.error
import urllib.request
from collections.abc import Callable
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

MODELS = ("jev", "clef", "clef-flash")
SELECTORS = {"jev": "jev-latest", "clef": "clef", "clef-flash": "clef-flash"}
SUITE = Path(__file__).resolve().parent / "decisionbench" / "synthetic.json"
MAX_INPUT = 2 * 1024 * 1024
MAX_RESPONSE = 1024 * 1024
PRICE_DATE = "2026-10-07"
PRICES = {"clef": 0.24, "clef-flash": 0.09}
LEVELS = [
    "Nothing related to the incident; can be skipped",
    "Only indirect or routine information",
    "Shows symptoms or effects of the incident",
    "Shows the cause of the incident or its direct evidence; examine this first",
]


class BenchError(Exception):
    """Only fixed error codes may leave the network/credential boundary."""


@dataclass(frozen=True)
class Case:
    identifier: str
    state: Any
    questions: dict[str, Any]
    grades: dict[str, int] | None

    def common(self) -> dict[str, Any]:
        return {"state": self.state, "questions": self.questions}

    def fingerprint(self) -> str:
        return hashlib.sha256(encode(self.common())).hexdigest()


@dataclass(frozen=True)
class Credentials:
    token: str
    account: str = ""


def encode(value: Any) -> bytes:
    return json.dumps(
        value,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
        allow_nan=False,
    ).encode("utf-8")


def no_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise BenchError("JSON_DUPLICATE_KEY")
        result[key] = value
    return result


def decode(payload: bytes) -> Any:
    try:
        return json.loads(
            payload,
            object_pairs_hook=no_duplicates,
            parse_constant=lambda _: (_ for _ in ()).throw(
                BenchError("JSON_NONFINITE")
            ),
        )
    except (ValueError, UnicodeError, RecursionError):
        raise BenchError("JSON_INVALID") from None


def number(value: Any, minimum: float, maximum: float) -> bool:
    return (
        type(value) in (int, float)
        and minimum <= value <= maximum
        and math.isfinite(value)
    )


def load_suite(path: Path) -> list[Case]:
    with path.open("rb") as stream:
        payload = stream.read(MAX_INPUT + 1)
    if len(payload) > MAX_INPUT:
        raise BenchError("SUITE_TOO_LARGE")
    raw = decode(payload)
    if (
        not isinstance(raw, dict)
        or set(raw) != {"schema_version", "cases"}
        or type(raw["schema_version"]) is not int
        or raw["schema_version"] != 1
    ):
        raise BenchError("SUITE_SCHEMA")
    entries = raw["cases"]
    if not isinstance(entries, list) or not 1 <= len(entries) <= 100:
        raise BenchError("CASE_COUNT")
    cases: list[Case] = []
    seen: set[str] = set()
    for entry in entries:
        if (
            not isinstance(entry, dict)
            or not {"id", "state", "questions"} <= set(entry)
            or set(entry) - {"id", "state", "questions", "grades"}
        ):
            raise BenchError("CASE_SCHEMA")
        identifier = entry["id"]
        if (
            not isinstance(identifier, str)
            or not re.fullmatch(r"[A-Za-z0-9_.-]{1,80}", identifier)
            or identifier in seen
        ):
            raise BenchError("CASE_ID")
        seen.add(identifier)
        questions = entry["questions"]
        if not isinstance(questions, dict) or not 1 <= len(questions) <= 64:
            raise BenchError("QUESTION_COUNT")
        for key, question in questions.items():
            if not re.fullmatch(r"[A-Za-z0-9_.-]{1,100}", key) or not isinstance(
                question, dict
            ):
                raise BenchError("QUESTION_SCHEMA")
            if (
                set(question) != {"type", "instructions", "criteria"}
                or question["type"] != "score"
            ):
                raise BenchError("QUESTION_SCHEMA")
            if (
                not isinstance(question["instructions"], str)
                or not 1 <= len(question["instructions"]) <= 4000
            ):
                raise BenchError("QUESTION_INSTRUCTIONS")
            if question["criteria"] != LEVELS:
                raise BenchError("QUESTION_LEVELS")
        grades = entry.get("grades")
        if grades is not None and (
            not isinstance(grades, dict)
            or set(grades) != set(questions)
            or not all(type(v) is int and 0 <= v <= 3 for v in grades.values())
            or max(grades.values()) == 0
        ):
            raise BenchError("GRADES_SCHEMA")
        if not isinstance(entry["state"], (str, dict, list)) or not entry["state"]:
            raise BenchError("STATE_SCHEMA")
        case = Case(identifier, entry["state"], questions, grades)
        if len(encode(case.common())) > MAX_INPUT:
            raise BenchError("REQUEST_TOO_LARGE")
        cases.append(case)
    return cases


def credentials(models: list[str], environ: dict[str, str]) -> dict[str, Credentials]:
    required = []
    if "jev" in models:
        required.append("JEV_API_KEY")
    if any(model != "jev" for model in models):
        required.extend(["CLOUDFLARE_AUTH_TOKEN", "CLOUDFLARE_ACCOUNT_ID"])
    if any(not environ.get(key, "").strip() for key in required):
        raise BenchError(
            "CREDENTIALS_MISSING:"
            + ",".join(key for key in required if not environ.get(key, "").strip())
        )
    for key in required:
        value = environ[key]
        if value != value.strip() or any(
            ord(char) < 33 or ord(char) > 126 for char in value
        ):
            raise BenchError("CREDENTIALS_FORMAT:" + key)
    account = environ.get("CLOUDFLARE_ACCOUNT_ID", "")
    if any(model != "jev" for model in models) and not re.fullmatch(
        r"[0-9a-fA-F]{32}", account
    ):
        raise BenchError("ACCOUNT_ID_FORMAT")
    return {
        model: Credentials(environ["JEV_API_KEY"])
        if model == "jev"
        else Credentials(environ["CLOUDFLARE_AUTH_TOKEN"], account)
        for model in models
    }


def endpoint(model: str, credential: Credentials) -> str:
    if model == "jev":
        return "https://api.typesafe.ai/v1/systemone"
    if not re.fullmatch(r"[0-9a-fA-F]{32}", credential.account):
        raise BenchError("ACCOUNT_ID_FORMAT")
    return f"https://api.cloudflare.com/client/v4/accounts/{credential.account}/ai/run/@cf/cloudflare/{model}"


def request_body(case: Case, model: str) -> bytes:
    return encode({"model": SELECTORS[model], **case.common()})


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(
        self, req: Any, fp: Any, code: int, msg: str, headers: Any, newurl: str
    ) -> None:
        return None


def transmit(url: str, body: bytes, credential: Credentials, timeout: float) -> bytes:
    # Environment proxies and redirects would add an unselected recipient.
    opener = urllib.request.build_opener(
        urllib.request.ProxyHandler({}),
        NoRedirect(),
        urllib.request.HTTPSHandler(context=ssl.create_default_context()),
    )
    request = urllib.request.Request(
        url,
        body,
        {
            "Authorization": "Bearer " + credential.token,
            "Content-Type": "application/json",
        },
        method="POST",
    )
    try:
        with opener.open(request, timeout=timeout) as response:
            if response.status != 200:
                raise BenchError("HTTP_STATUS:" + str(response.status))
            payload = read_response(response, timeout)
    except urllib.error.HTTPError as error:
        status = error.code
        error.close()
        raise BenchError("HTTP_STATUS:" + str(status)) from None
    except (urllib.error.URLError, TimeoutError, OSError, http.client.HTTPException):
        raise BenchError("NETWORK_ERROR") from None
    if len(payload) > MAX_RESPONSE:
        raise BenchError("RESPONSE_TOO_LARGE")
    return payload


def read_response(response: Any, timeout: float) -> bytes:
    deadline = time.monotonic() + timeout
    chunks: list[bytes] = []
    length = 0
    while True:
        if time.monotonic() >= deadline:
            raise BenchError("RESPONSE_DEADLINE")
        chunk = response.read1(min(65536, MAX_RESPONSE + 1 - length))
        if not chunk:
            break
        chunks.append(chunk)
        length += len(chunk)
        if length > MAX_RESPONSE:
            raise BenchError("RESPONSE_TOO_LARGE")
    return b"".join(chunks)


def parse_response(payload: bytes, case: Case, model: str) -> dict[str, Any]:
    decoded = decode(payload)
    if not isinstance(decoded, dict):
        raise BenchError("RESPONSE_SCHEMA")
    if model != "jev":
        if decoded.get("success") is not True or not isinstance(
            decoded.get("result"), dict
        ):
            raise BenchError("CLOUDFLARE_ENVELOPE")
        decoded = decoded["result"]
    actual_model = decoded.get("model")
    if not isinstance(actual_model, str) or not re.fullmatch(
        r"[A-Za-z0-9@/_.:-]{1,100}", actual_model
    ):
        raise BenchError("MODEL_SCHEMA")
    if model == "jev" and not actual_model.startswith("jev"):
        raise BenchError("MODEL_MISMATCH")
    if model != "jev" and actual_model not in {
        model,
        "@cf/cloudflare/" + model,
        model + "-mock",
    }:
        raise BenchError("MODEL_MISMATCH")
    answers = decoded.get("answers")
    if not isinstance(answers, dict) or set(answers) != set(case.questions):
        raise BenchError("ANSWER_IDS")
    scores: dict[str, dict[str, float]] = {}
    for key in case.questions:
        answer = answers[key]
        if not isinstance(answer, dict) or answer.get("type") != "score":
            raise BenchError("ANSWER_TYPE")
        if not number(answer.get("score"), 0, 3) or not number(
            answer.get("confidence"), 0, 1
        ):
            raise BenchError("ANSWER_RANGE")
        scores[key] = {"score": answer["score"], "confidence": answer["confidence"]}
    usage = decoded.get("usage")
    if (
        not isinstance(usage, dict)
        or type(usage.get("input_tokens")) is not int
        or not 0 <= usage["input_tokens"] <= MAX_INPUT * 4
    ):
        raise BenchError("USAGE_SCHEMA")
    ranked = sorted(scores, key=lambda key: -scores[key]["score"])
    return {
        "actual_model": actual_model,
        "scores": scores,
        "ranking": ranked,
        "input_tokens": usage["input_tokens"],
        "quality": quality(case.grades, ranked, scores),
    }


def quality(
    grades: dict[str, int] | None, ranked: list[str], scores: dict[str, Any]
) -> dict[str, Any] | None:
    if grades is None:
        return None
    best = max(grades.values())
    ideal = sorted(grades, key=lambda key: -grades[key])

    def dcg(order: list[str]) -> float:
        return sum(
            (2 ** grades[key] - 1) / math.log2(index + 2)
            for index, key in enumerate(order)
        )

    top_score = scores[ranked[0]]["score"]
    return {
        "top1": grades[ranked[0]] == best,
        "top3": any(grades[key] == best for key in ranked[:3]),
        "ndcg": dcg(ranked) / dcg(ideal),
        "top_tie_count": sum(value["score"] == top_score for value in scores.values()),
    }


def schedule(
    cases: list[Case], models: list[str], repeats: int, warmup: int
) -> list[tuple[Case, str, int, bool]]:
    jobs: list[tuple[Case, str, int, bool]] = []
    for repetition in range(warmup + repeats):
        for index, case in enumerate(cases):
            offset = (repetition + index) % len(models)
            order = models[offset:] + models[:offset]
            for model in order:
                jobs.append((case, model, repetition - warmup + 1, repetition < warmup))
    return jobs


def percentile(values: list[float], quantile: float) -> float | None:
    if not values:
        return None
    return sorted(values)[math.ceil(quantile * len(values)) - 1]


def statistics_for(records: list[dict[str, Any]]) -> dict[str, Any]:
    measured = [record for record in records if not record["warmup"]]
    successful = [record for record in measured if record["status"] == "PASS"]
    qualities = [
        record["quality"] for record in successful if record["quality"] is not None
    ]
    latencies = [record["elapsed_seconds"] for record in successful]
    costs = [record["estimated_usd"] for record in successful]
    return {
        "attempts": len(measured),
        "successes": len(successful),
        "warmup_attempts": sum(r["warmup"] for r in records),
        "warmup_failures": sum(r["warmup"] and r["status"] == "FAIL" for r in records),
        "failures": len(measured) - len(successful),
        "quality_samples": len(qualities),
        "p50_seconds": statistics.median(latencies) if latencies else None,
        "p95_seconds": percentile(latencies, 0.95),
        "min_seconds": min(latencies) if latencies else None,
        "max_seconds": max(latencies) if latencies else None,
        "input_tokens": sum(record["input_tokens"] for record in successful),
        "estimated_usd": sum(costs)
        if successful and all(cost is not None for cost in costs)
        else None,
        "top1_rate": statistics.mean(q["top1"] for q in qualities)
        if qualities
        else None,
        "top3_rate": statistics.mean(q["top3"] for q in qualities)
        if qualities
        else None,
        "mean_ndcg": statistics.mean(q["ndcg"] for q in qualities)
        if qualities
        else None,
        "top_ties": sum(q["top_tie_count"] > 1 for q in qualities),
        "successful_cost_including_warmup_usd": (
            sum(r["estimated_usd"] for r in records if r["status"] == "PASS")
            if any(r["status"] == "PASS" for r in records)
            and all(
                r["estimated_usd"] is not None for r in records if r["status"] == "PASS"
            )
            else None
        ),
    }


def summarize(
    records: list[dict[str, Any]], metadata: dict[str, Any]
) -> dict[str, Any]:
    return {
        "mode": metadata["mode"],
        "status": "FAIL"
        if any(r["status"] == "FAIL" for r in records)
        else (
            "PASS" if len(records) == metadata["planned_requests"] else "IN_PROGRESS"
        ),
        "planned_requests": metadata["planned_requests"],
        "completed_requests": len(records),
        "not_run_requests": metadata["planned_requests"] - len(records),
        "models": {
            model: statistics_for([r for r in records if r["model"] == model])
            for model in metadata["models"]
        },
        "conditions": {
            key: metadata[key]
            for key in (
                "repeats",
                "warmup_per_case",
                "timeout_seconds",
                "connection_policy",
                "region",
                "plan",
                "price_date",
                "prices_usd_per_million_input_tokens",
            )
        },
        "actual_models": {
            model: sorted(
                {
                    r["actual_model"]
                    for r in records
                    if r["model"] == model and "actual_model" in r
                }
            )
            for model in metadata["models"]
        },
        "cases": {
            case_id: {
                model: statistics_for(
                    [r for r in records if r["case"] == case_id and r["model"] == model]
                )
                for model in metadata["models"]
            }
            for case_id in metadata["case_ids"]
        },
    }


def write_new(path: Path, content: bytes) -> None:
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(content)


def render_report(summary: dict[str, Any]) -> tuple[str, str]:
    def value(number_value: Any) -> str:
        return "不明" if number_value is None else f"{number_value:.4f}"

    lines = [
        "# 判断AI比較結果",
        "",
        f"モード：{summary['mode']}、実行状態：{summary['status']}",
        "",
        "mockはツール確認用の合成応答です。実モデルの性能・品質・課金を表しません。"
        if summary["mode"] == "mock"
        else "速度は成功応答のHTTP往復と契約検査の時間です。ログ収集時間を含みません。",
        "",
        f"計画{summary['planned_requests']}件、記録{summary['completed_requests']}件、未実施{summary['not_run_requests']}件。",
        "",
        "warmupと失敗は成功速度・品質の母集団に含めません。合成ラベルへの一致であり、実障害の原因特定精度ではありません。",
        "",
        "| モデル | 成功/試行 | p50秒 | p95秒 | Top1 | Top3 | nDCG | 推計USD |",
        "|---|---:|---:|---:|---:|---:|---:|---:|",
    ]
    rows: list[list[str]] = []
    for model, stat in summary["models"].items():
        row = [
            model,
            f"{stat['successes']}/{stat['attempts']}",
            value(stat["p50_seconds"]),
            value(stat["p95_seconds"]),
            value(stat["top1_rate"]),
            value(stat["top3_rate"]),
            value(stat["mean_ndcg"]),
            value(stat["estimated_usd"]),
        ]
        rows.append(row)
        lines.append("| " + " | ".join(row) + " |")
    lines.extend(
        [
            "",
            "## ケース別の結果",
            "",
            "| ケース | モデル | 成功/試行 | p50秒 | Top1 | Top3 | nDCG | 首位同点件数 |",
            "|---|---|---:|---:|---:|---:|---:|---:|",
        ]
    )
    case_rows: list[list[str]] = []
    for case_id, models in summary["cases"].items():
        for model, stat in models.items():
            row = [
                case_id,
                model,
                f"{stat['successes']}/{stat['attempts']}",
                value(stat["p50_seconds"]),
                value(stat["top1_rate"]),
                value(stat["top3_rate"]),
                value(stat["mean_ndcg"]),
                str(stat["top_ties"]),
            ]
            case_rows.append(row)
            lines.append("| " + " | ".join(row) + " |")
    lines.extend(
        [
            "",
            "同scoreの順位はsuiteの質問定義順。Top1/Top3はこの同点処理に従い、首位同点も別記します。",
            "試行0は未実施。ケース別の順位・失敗理由と条件はsummary.jsonとsamples.jsonlを確認してください。",
            "費用は成功応答の入力tokens×記録した単価の推計。無料枠・契約料・失敗時課金は含まず、請求額ではありません。",
            "",
        ]
    )
    table = (
        "<table><thead><tr>"
        + "".join(
            "<th>" + html.escape(s) + "</th>"
            for s in [
                "モデル",
                "成功/試行",
                "p50秒",
                "p95秒",
                "Top1",
                "Top3",
                "nDCG",
                "推計USD",
            ]
        )
        + "</tr></thead><tbody>"
    )
    table += (
        "".join(
            "<tr>"
            + "".join("<td>" + html.escape(cell) + "</td>" for cell in row)
            + "</tr>"
            for row in rows
        )
        + "</tbody></table>"
    )
    table += (
        "<h2>ケース別の結果</h2><table><thead><tr>"
        + "".join(
            "<th>" + html.escape(label) + "</th>"
            for label in [
                "ケース",
                "モデル",
                "成功/試行",
                "p50秒",
                "Top1",
                "Top3",
                "nDCG",
                "首位同点件数",
            ]
        )
        + "</tr></thead><tbody>"
    )
    table += (
        "".join(
            "<tr>"
            + "".join("<td>" + html.escape(cell) + "</td>" for cell in row)
            + "</tr>"
            for row in case_rows
        )
        + "</tbody></table>"
    )
    paragraphs = "".join(
        "<p>" + html.escape(line) + "</p>"
        for line in lines[2:]
        if line and not line.startswith("|")
    )
    page = (
        '<!doctype html><html lang="ja"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">'
        "<title>判断AI比較結果</title><style>body{font-family:system-ui;line-height:1.8;margin:20px}table{border-collapse:collapse}th,td{border:1px solid #aaa;padding:8px}.scroll{overflow:auto}</style>"
        "<h1>判断AI比較結果</h1>"
        + paragraphs
        + '<div class="scroll">'
        + table
        + "</div></html>"
    )
    return "\n".join(lines), page


def mock_payload(case: Case, model: str) -> bytes:
    answers = {
        key: {
            "type": "score",
            "score": (case.grades or {}).get(key, 1),
            "confidence": 0.9,
        }
        for key in case.questions
    }
    result = {
        "model": SELECTORS[model] + "-mock",
        "answers": answers,
        "usage": {"input_tokens": max(1, len(encode(case.common())) // 4)},
    }
    return encode(
        result
        if model == "jev"
        else {"success": True, "result": result, "errors": [], "messages": []}
    )


def run(args: argparse.Namespace, transport: Callable[..., bytes] = transmit) -> int:
    models = args.models.split(",")
    if (
        not models
        or len(set(models)) != len(models)
        or any(model not in MODELS for model in models)
    ):
        raise BenchError("MODELS_INVALID")
    if (
        not 1 <= args.repeats <= 100
        or not 0 <= args.warmup <= 3
        or not number(args.timeout, 1, 120)
    ):
        raise BenchError("RUN_LIMITS")
    if args.jev_price is not None and not number(args.jev_price, 0, 1000):
        raise BenchError("PRICE_INVALID")
    cases = load_suite(args.suite)
    if args.cases:
        selected = args.cases.split(",")
        if len(set(selected)) != len(selected) or not set(selected) <= {
            case.identifier for case in cases
        }:
            raise BenchError("CASE_SELECTION")
        cases = [case for case in cases if case.identifier in selected]
    jobs = schedule(cases, models, args.repeats, args.warmup)
    if len(jobs) > 1000:
        raise BenchError("REQUEST_COUNT_LIMIT")
    mode = "live" if args.live else "mock" if args.mock else "preview"
    if mode == "live" and (
        args.max_requests is None
        or not 1 <= args.max_requests <= 1000
        or len(jobs) > args.max_requests
    ):
        raise BenchError("EXPLICIT_REQUEST_BUDGET_REQUIRED")
    if mode == "live" and (not args.region or not args.plan):
        raise BenchError("REGION_AND_PLAN_REQUIRED")
    for label in (args.region, args.plan):
        if label is not None and not re.fullmatch(r"[A-Za-z0-9_.-]{1,80}", label):
            raise BenchError("ENVIRONMENT_LABEL_INVALID")
    metadata: dict[str, Any] = {
        "schema_version": 1,
        "mode": mode,
        "started_at": datetime.now(timezone.utc).isoformat(),
        "models": models,
        "selectors": {m: SELECTORS[m] for m in models},
        "planned_requests": len(jobs),
        "repeats": args.repeats,
        "warmup_per_case": args.warmup,
        "timeout_seconds": args.timeout,
        "automatic_retries": 0,
        "connection_policy": "new-connection-per-request",
        "region": args.region,
        "plan": args.plan,
        "python": platform.python_version(),
        "case_ids": [case.identifier for case in cases],
        "suite_sha256": hashlib.sha256(
            encode(
                [
                    {
                        **case.common(),
                        "id": case.identifier,
                        "grades": case.grades,
                        "question_order": list(case.questions),
                    }
                    for case in cases
                ]
            )
        ).hexdigest(),
        "cases": [
            {
                "id": c.identifier,
                "common_sha256": c.fingerprint(),
                "common_bytes": len(encode(c.common())),
                "question_count": len(c.questions),
                "question_order": list(c.questions),
                "labeled": c.grades is not None,
                "grades": c.grades,
            }
            for c in cases
        ],
        "prices_usd_per_million_input_tokens": {**PRICES, "jev": args.jev_price},
        "price_date": PRICE_DATE,
        "tool_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
        "destinations": {
            m: "api.typesafe.ai/v1/systemone"
            if m == "jev"
            else f"api.cloudflare.com/accounts/<account_id>/ai/run/@cf/cloudflare/{m}"
            for m in models
        },
    }
    if mode == "preview":
        print(json.dumps(metadata, ensure_ascii=False, indent=2))
        return 0
    if args.output is None:
        raise BenchError("OUTPUT_REQUIRED")
    authorized = credentials(models, dict(os.environ)) if mode == "live" else {}
    # Exclusive creation preserves existing evidence and gives every run a private directory.
    if args.output.is_symlink() or any(
        parent.is_symlink() for parent in args.output.parents
    ):
        raise BenchError("OUTPUT_SYMLINK")
    args.output.mkdir(mode=0o700, parents=False, exist_ok=False)
    write_new(args.output / "metadata.json", encode(metadata))
    records: list[dict[str, Any]] = []
    actual_models: dict[str, str] = {}
    exit_code = 0
    with (args.output / "samples.jsonl").open("x", encoding="utf-8") as stream:
        os.chmod(args.output / "samples.jsonl", 0o600)
        for sequence, (case, model, repetition, warmup) in enumerate(jobs, 1):
            record: dict[str, Any] = {
                "sequence": sequence,
                "case": case.identifier,
                "model": model,
                "repetition": repetition,
                "warmup": warmup,
                "common_sha256": case.fingerprint(),
                "request_sha256": hashlib.sha256(request_body(case, model)).hexdigest(),
                "started_at": datetime.now(timezone.utc).isoformat(),
            }
            started = time.perf_counter()
            try:
                payload = (
                    mock_payload(case, model)
                    if mode == "mock"
                    else transport(
                        endpoint(model, authorized[model]),
                        request_body(case, model),
                        authorized[model],
                        args.timeout,
                    )
                )
                record.update(parse_response(payload, case, model))
                if (
                    model in actual_models
                    and actual_models[model] != record["actual_model"]
                ):
                    raise BenchError("MODEL_CHANGED")
                actual_models[model] = record["actual_model"]
                record["status"] = "PASS"
                price = metadata["prices_usd_per_million_input_tokens"][model]
                record["estimated_usd"] = (
                    record["input_tokens"] * price / 1_000_000
                    if price is not None
                    else None
                )
            except BenchError as error:
                record.update(status="FAIL", error_code=str(error))
                exit_code = 1
            except KeyboardInterrupt:
                record.update(status="FAIL", error_code="INTERRUPTED")
                exit_code = 130
            record["elapsed_seconds"] = time.perf_counter() - started
            records.append(record)
            stream.write(encode(record).decode("utf-8") + "\n")
            stream.flush()
            os.fsync(stream.fileno())
            print(
                f"{sequence}/{len(jobs)} {case.identifier} {model} {record['status']}",
                flush=True,
            )
            if exit_code:
                break
    summary = summarize(records, metadata)
    write_new(args.output / "summary.json", encode(summary))
    report, page = render_report(summary)
    write_new(args.output / "report.md", report.encode("utf-8"))
    write_new(args.output / "report.htm", page.encode("utf-8"))
    return exit_code


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(
        description="同一ログでJev/Clef/Clef-flashを比較。無指定は送信しないpreview。"
    )
    mode = result.add_mutually_exclusive_group()
    mode.add_argument(
        "--live", action="store_true", help="実APIに入力を送信（課金される場合あり）"
    )
    mode.add_argument(
        "--mock",
        action="store_true",
        help="合成応答でツールだけ確認（性能測定ではない）",
    )
    result.add_argument("--suite", type=Path, default=SUITE, help="固定入力suite JSON")
    result.add_argument("--models", default=",".join(MODELS))
    result.add_argument("--cases", help="測定するケースIDをカンマ指定。未指定は全件")
    result.add_argument("--repeats", type=int, default=3)
    result.add_argument(
        "--warmup",
        type=int,
        default=0,
        help="ケース×モデルごとの追加呼出し数。測定母集団から除外",
    )
    result.add_argument("--timeout", type=float, default=20)
    result.add_argument(
        "--output", type=Path, help="存在しない出力ディレクトリ。親は事前に用意"
    )
    result.add_argument(
        "--max-requests", type=int, help="live時必須。warmup込み総呼出し上限"
    )
    result.add_argument("--region", help="クライアント地域（例jp）。live時必須")
    result.add_argument("--plan", help="利用plan識別（例cf-free）。live時必須")
    result.add_argument(
        "--jev-price", type=float, help="Jevの100万入力token単価USD。未指定は費用不明"
    )
    return result


def main() -> int:
    try:
        return run(parser().parse_args())
    except BenchError as error:
        print("ERROR: " + str(error), file=sys.stderr)
        return 2
    except (OSError, ValueError, RecursionError):
        print(
            "ERROR: LOCAL_IO_OR_INPUT_ERROR（既存結果は上書きしません）",
            file=sys.stderr,
        )
        return 2
    except KeyboardInterrupt:
        print("ERROR: INTERRUPTED（部分記録はsamples.jsonlを確認）", file=sys.stderr)
        return 130


if __name__ == "__main__":
    sys.exit(main())
