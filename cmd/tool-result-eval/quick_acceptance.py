#!/usr/bin/env python3
"""Three single-attempt recovery tasks. Coverage always needs independent review."""
import argparse
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import datetime, timezone
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import time

WORKSPACE = Path(__file__).resolve().parents[3]
POGO = WORKSPACE / "pogo"
JOBS = (
    ("compact-persist-glm", "glm-5.3", "compact-persist", None),
    ("compact-persist-kimi", "k3", "compact-persist", None),
    ("switch-kimi-glm", "k3", "switch", "glm-5.3"),
)
KEYS = {"recovered_retry", "root_cause", "parent_propagation", "final_failure"}
ANSWER_FIELDS = {"trace_id", "root_cause_code", "root_cause_span_id", "request_span_id",
                 "root_cause_time", "request_failure_time", "recovered_noise_code",
                 "evidence", "coverage", "sources"}


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def freeze(binary, providers):
    return {"binary_sha256": sha(binary), "providers_sha256": sha(providers),
            "runner_sha256": sha(Path(__file__)),
            "go_sources": {str(p.relative_to(POGO)): sha(p) for p in sorted(POGO.rglob("*.go"))
                           if not any(x in (".git", "node_modules") for x in p.parts)}}


def command(job, binary, providers, output):
    name, model, phase, switched = job
    args = [str(binary), "--providers", str(providers), "--model", model,
            "--scenario", "recovery", "--recovery-phase", phase, "--budget-mode", "auto",
            "--repeats", "1", "--coverage-check", "exact", "--output", str(output / name)]
    if switched:
        args += ["--recovery-switch-model", switched]
    return args


def successful(request):
    return not request.get("error_class") and request.get("stop_reason") not in (None, "", "aborted")


def delivered_keys(late, result_id):
    """Use only successful actual late requests bound to successful late native reads."""
    calls = {c.get("id"): c for c in late.get("tool_calls", [])
             if c.get("name") == "tool_result" and not c.get("is_error")}
    keys = set()
    for request in late.get("requests", []):
        if not successful(request):
            continue
        for view in request.get("tool_views", []):
            call = calls.get(view.get("call_id"))
            obs = view.get("log_observation") or {}
            args = (call or {}).get("args") or {}
            if (not call or args.get("result_id") != result_id or obs.get("result_id") != result_id
                    or not obs.get("status_known") or obs.get("original_is_error")
                    or not obs.get("stored_text_complete")):
                continue
            if (obs.get("kind") == "search" and args.get("view") == "search"
                    and obs.get("source_exact") and obs.get("search_page_valid")
                    and obs.get("fragment_sha256") == view.get("body_sha256")
                    and obs.get("pattern") == args.get("pattern")
                    and obs.get("search_offset", 0) == args.get("offset", 0)):
                for part in obs.get("ranges", []):
                    if part.get("source_exact") and part.get("literal_exact") and part.get("context_end", 0) > part.get("context_start", 0):
                        keys.update(part.get("evidence_keys") or [])
            elif (obs.get("kind") == "text_range" and obs.get("source_exact")
                  and obs.get("end", 0) > obs.get("start", 0)
                  and (args.get("view") == "text" or
                       args.get("view") == "native" and args.get("pointer") == "/content/0/text")):
                keys.update(obs.get("evidence_keys") or [])
    return keys


def assess(summary, job):
    """Core only. Never interprets coverage or upgrades the binary's raw verdict."""
    checks = {"finished_single_run": summary.get("status") == "finished" and len(summary.get("runs", [])) == 1,
              "exact_diagnostic_only_no_grader": summary.get("coverage_check") == "exact"}
    if not checks["finished_single_run"]:
        return {"core_pass": False, "checks": checks, "scope_review": "pending"}
    run = summary["runs"][0]
    late, boundary = run.get("late", {}), run.get("boundary", {})
    source, early = run.get("source_check", {}), run.get("early_evidence", {})
    evidence, recovery = late.get("log_evidence", {}), late.get("host_recovery", {})
    stages = run.get("stages", [])
    result_id = run.get("original_result_id", "")
    answer = None
    try:
        text = late.get("final_answer", "").strip()
        if text.startswith("```") and text.endswith("```"):
            text = text.split("\n", 1)[1][:-3].strip()
        answer = json.loads(text)
    except (ValueError, IndexError):
        pass
    expected = summary.get("expected", {})
    checks.update({
        "configuration": run.get("scenario") == "recovery" and run.get("phase") == job[2]
            and run.get("model") == job[1] and run.get("budget_mode") == "auto" and run.get("repeat") == 1,
        "completed_without_task_error": not run.get("error_class") and not late.get("error_class")
            and late.get("end_reason") == "completed" and bool(stages)
            and all(s.get("end_reason") == "completed" and not s.get("error_class") for s in stages),
        "valid_result_id": re.fullmatch(r"R[0-9a-f]{64}", result_id) is not None,
        "single_original_mcp_no_file_bypass": run.get("mcp_original_call_attempts") == 1
            and late.get("mcp_original_call_attempts") == 1 and run.get("file_tool_calls") == 0 and late.get("file_tool_calls") == 0,
        "early_middle_not_leaked": early.get("passed") is True and early.get("delivered_middle_evidence") == [],
        "strict_json": late.get("final_format_correct") is True and isinstance(answer, dict)
            and set(answer) == ANSWER_FIELDS and answer == late.get("log_answer"),
        "facts_and_four_verbatim_quotes": late.get("fact_checks_run") is True and late.get("numeric_correct") is True
            and isinstance(answer, dict) and all(answer.get(k) == expected.get(k) for k in ANSWER_FIELDS - {"coverage", "sources"})
            and isinstance(answer.get("evidence"), list) and len(answer["evidence"]) == 4,
        "actual_late_four_evidence": evidence.get("passed") is True
            and set(evidence.get("required_evidence") or []) == KEYS
            and set(evidence.get("delivered_evidence") or []) == KEYS and evidence.get("missing_evidence") == []
            and delivered_keys(late, result_id) == KEYS,
        "saved_bytes_recoverable": recovery.get("checked") is True and recovery.get("complete") is True
            and recovery.get("matches_source") is True and not recovery.get("error_class")
            and recovery.get("text_sha256") == summary.get("dataset_text_sha256")
            and recovery.get("text_bytes") == summary.get("dataset_text_bytes"),
        "source_stamp_and_lineage": source.get("passed") is True and source.get("original_stamp_retained") is True
            and bool(source.get("original_before")) and source.get("original_before") == source.get("original_after")
            and source.get("violations") == []
            and all(p == source["original_before"] for p in source.get("parent_stamps") or []),
        "citation_format_and_labels": late.get("citations_known") is True
            and late.get("citation_labels_known") is True and late.get("citation_format_correct") is True,
        "boundary_model": boundary.get("model_before") == job[1] and boundary.get("model_after") == (job[3] or job[1]),
        "real_warmup_and_late": {s.get("name") for s in stages} >= {"early-fetch", "background-1", "background-2", "background-3", "late-analysis"}
            and all(any(successful(r) for r in s.get("requests", [])) for s in stages),
    })
    if job[2] == "compact-persist":
        checks["compact_committed_and_reopened"] = (
            boundary.get("compact_called") is True and boundary.get("compact_committed") is True
            and boundary.get("persisted_round_trip") is True and boundary.get("registry_rebuilt") is True
            and boundary.get("agent_rebuilt") is True and boundary.get("persisted", {}).get("sha256")
            == boundary.get("reopened", {}).get("sha256") and bool(boundary.get("session_file_sha256"))
            and boundary.get("summary", {}).get("root_cause_leaked") is False)
    else:
        checks["real_model_switch"] = boundary.get("client_rebuilt") is True and boundary.get("model_changed") is True
    return {"core_pass": all(checks.values()), "checks": checks,
            "failed_checks": [k for k, v in checks.items() if not v],
            "raw_passed_unchanged": summary.get("passed"), "raw_run_passed_unchanged": run.get("passed"),
            "legacy_coverage_exact_match": late.get("coverage_exact_match", late.get("coverage_correct")),
            "coverage": answer.get("coverage") if isinstance(answer, dict) else None, "scope_review": "pending",
            "overall_acceptance": "pending_scope_review"}


def run_one(job, binary, providers, output):
    destination = output / job[0]
    args = command(job, binary, providers, output)
    started = time.monotonic()
    result = subprocess.run(args, cwd=POGO, env=dict(os.environ), capture_output=True, text=True)
    destination.mkdir(parents=True, exist_ok=True)
    (destination / "process.log").write_text(result.stdout + result.stderr)
    process = {"name": job[0], "command": args, "exit_code": result.returncode,
               "wall_seconds": time.monotonic() - started, "completed_utc": datetime.now(timezone.utc).isoformat()}
    write(destination / "process.json", process)
    summary_path = destination / "summary.json"
    review = {"core_pass": False, "scope_review": "pending", "failed_checks": ["summary_missing"]}
    if summary_path.exists():
        review = assess(json.loads(summary_path.read_text()), job)
        review["summary_sha256"] = sha(summary_path)
    review["process_completed"] = result.returncode in (0, 1)
    review["core_pass"] = review["core_pass"] and review["process_completed"]
    write(destination / "core-review.json", review)
    print("END " + job[0] + " core=" + str(review["core_pass"]) + " scope=pending", flush=True)
    return {"name": job[0], **process, **review}


def self_check():
    # Small offline mutation checks. These are not real-model acceptance results.
    rid = "R" + "a" * 64
    late = {"tool_calls": [{"id": "c", "name": "tool_result", "args": {"result_id": rid, "view": "text"}}],
            "requests": [{"stop_reason": "completed", "tool_views": [{"call_id": "c", "log_observation": {
                "result_id": rid, "kind": "text_range", "status_known": True, "stored_text_complete": True,
                "source_exact": True, "start": 1, "end": 99, "evidence_keys": sorted(KEYS)}}]}]}
    assert delivered_keys(late, rid) == KEYS
    for mutation in ("error_class", "stop_reason"):
        bad = copy.deepcopy(late)
        bad["requests"][0][mutation] = "failed" if mutation == "error_class" else "aborted"
        assert delivered_keys(bad, rid) == set()
    assert delivered_keys(late, "R" + "b" * 64) == set()
    bad = copy.deepcopy(late)
    bad["tool_calls"][0]["is_error"] = True
    assert delivered_keys(bad, rid) == set()
    assert assess({"status": "running", "runs": [{}]}, JOBS[0])["core_pass"] is False
    print("Offline checks passed: actual successful requests, source identity, failed calls, unfinished reports; no model calls.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path)
    parser.add_argument("--providers", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--inspect", action="store_true", help="Print frozen inputs and commands; zero calls and writes")
    parser.add_argument("--self-check", action="store_true", help="Offline runner checks only")
    args = parser.parse_args()
    if args.self_check:
        self_check()
        return
    if not all((args.binary, args.providers, args.output)):
        parser.error("--binary, --providers and --output are required")
    binary, providers, output = args.binary.resolve(), args.providers.resolve(), args.output.resolve()
    frozen = freeze(binary, providers)
    plan = {"profile": "spill-three-task-core-plus-independent-scope-v1", "jobs": [command(j, binary, providers, output) for j in JOBS],
            "models": ["glm-5.3", "k3"], "max_parallel": 2, "attempts_per_job": 1, "grader_calls": 0,
            "timeout_policy": "unchanged binary defaults", "scope_review": "required_independent_actual_delivery_ledger",
            "old_calibration_reclassified": False, "docker_deployed": False, **frozen}
    if args.inspect:
        print(json.dumps({k: v for k, v in plan.items() if k != "go_sources"}, ensure_ascii=False, indent=2))
        print("Frozen Go files:", len(frozen["go_sources"]))
        return
    owned = [output / j[0] for j in JOBS] + [output / x for x in ("quick-manifest.json", "quick-results.json")]
    if any(p.exists() for p in owned):
        parser.error("Refuse rerun/overwrite of any quick job or runner artifact")
    output.mkdir(parents=True, exist_ok=True)
    write(output / "quick-manifest.json", plan)
    records = []
    with ThreadPoolExecutor(max_workers=2) as executor:
        futures = [executor.submit(run_one, j, binary, providers, output) for j in JOBS]
        for future in as_completed(futures):
            records.append(future.result())
            write(output / "quick-results.json", {"status": "running", "jobs": records, "scope_review": "pending"})
    unchanged = freeze(binary, providers) == frozen
    write(output / "quick-results.json", {"status": "finished", "jobs": records, "frozen_inputs_unchanged": unchanged,
          "core_pass": len(records) == 3 and all(r["core_pass"] for r in records) and unchanged,
          "scope_review": "pending", "overall_acceptance": "pending_scope_review", "docker_deployed": False})
    if not unchanged:
        raise SystemExit("Frozen inputs changed during tasks; results cannot establish this candidate's acceptance")


if __name__ == "__main__":
    main()
