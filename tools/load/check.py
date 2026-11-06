#!/usr/bin/env python3
"""Bounded local load test: real uploads, quotes, jobs, findings, exports and RLS."""
import argparse
from concurrent.futures import ThreadPoolExecutor, as_completed
import importlib.util
import json
import math
from pathlib import Path
import sys
import time
from urllib.parse import urlsplit
import uuid

import httpx
import yaml

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "sdks/python"))
from docmatch import Client, DocmatchError

spec = importlib.util.spec_from_file_location("verifier", ROOT / "tools/demos/verify_results.py")
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)


def percentile(values, p):
    return sorted(values)[max(0, math.ceil(len(values) * p) - 1)]


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--api", default="http://localhost:8080")
    ap.add_argument("--users", type=int, default=4)
    ap.add_argument("--runs", type=int, default=2)
    ap.add_argument("--max-p95-seconds", type=float, default=30)
    ap.add_argument("--report", default="scratch/audit/load.json")
    args = ap.parse_args()
    url = urlsplit(args.api)
    if url.scheme != "http" or url.hostname not in ("localhost", "127.0.0.1", "::1") or url.username or url.password or url.query or url.fragment:
        ap.error("use somente uma API de desenvolvimento HTTP em loopback")
    if not (1 <= args.users <= 8 and 1 <= args.runs <= 5):
        ap.error("limites locais: 1–8 usuários, 1–5 execuções por usuário")
    pack = ROOT / "testdata/golden/vantara-twin"
    expected = yaml.safe_load((pack / "expected.yaml").read_text())
    files = [p for p in sorted(pack.iterdir()) if p.suffix in (".csv", ".txt")]
    token = uuid.uuid4().hex
    report_path = ROOT / args.report
    report_path.parent.mkdir(parents=True, exist_ok=True)
    clients, keys, sessions = [], [], []
    durations, jobs, failures = [], [], []
    started = time.monotonic()
    try:
        for i in range(args.users):
            session = httpx.Client(base_url=args.api, timeout=30, follow_redirects=False, trust_env=False)
            sessions.append(session)
            methods = session.get("/v1/auth/methods"); methods.raise_for_status()
            if methods.json().get("dev") is not True:
                raise RuntimeError("O teste exige login de desenvolvimento; recusando outro ambiente.")
            login = session.post("/v1/auth/dev/login", json={"email": f"load-{token}-{i}@docmatch.test"})
            login.raise_for_status()
            created = session.post("/v1/api-keys", json={"label": "Local load test", "lane": "api_batch", "scopes": ["uploads:write", "runs:write", "runs:read"]})
            created.raise_for_status(); key = created.json(); keys.append(key["id"])
            clients.append(Client(args.api, key["key"]))

        def worker(index):
            client = clients[index]
            results = []
            for _ in range(args.runs):
                before = time.monotonic()
                uploads = [client.upload(p.name, p.read_bytes(), "text/csv" if p.suffix == ".csv" else "text/plain")["id"] for p in files]
                request = client.input(uploads)
                quote = client.estimate(request)
                run = client.run(quote, request)
                done = client.wait(run["id"], timeout=120, poll_interval=.25)
                findings = client.findings(run["id"])
                score = verifier.verify(expected, done, findings)
                if not score["passed"]:
                    raise AssertionError("Dados divergentes sob concorrência")
                if index == 0 and not results:
                    report_path.with_suffix(".snapshot.json").write_text(json.dumps({"run": done, "findings": findings}))
                exported = client.export(run["id"])
                if not exported.startswith(b"PK"):
                    raise AssertionError("Exportação XLSX inválida")
                results.append((time.monotonic() - before, run["id"], index))
            return results

        with ThreadPoolExecutor(max_workers=args.users) as pool:
            futures = [pool.submit(worker, i) for i in range(args.users)]
            for future in as_completed(futures):
                try:
                    for duration, run_id, owner in future.result():
                        durations.append(duration); jobs.append((run_id, owner))
                except Exception as error:
                    failures.append(type(error).__name__ + ": " + str(error))
        if len(clients) > 1:
            for run_id, owner in jobs:
                try:
                    clients[(owner + 1) % len(clients)].get_run(run_id)
                    failures.append("Isolamento entre organizações violado")
                except DocmatchError as error:
                    if error.status != 404: failures.append(f"Isolamento respondeu {error.status}")
        p95 = percentile(durations, .95) if durations else None
        if p95 is not None and p95 > args.max_p95_seconds:
            failures.append("p95 acima do orçamento configurado")
        report = {"users": args.users, "expected_runs": args.users * args.runs, "completed": len(durations),
                  "p95_seconds": p95, "elapsed_seconds": time.monotonic() - started,
                  "all_results_score_100": len(durations) == args.users * args.runs,
                  "cross_tenant_checks": len(jobs) if len(clients) > 1 else 0, "failures": failures}
        report_path.write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report, indent=2))
        return 1 if failures or len(durations) != args.users * args.runs else 0
    finally:
        for session, key in zip(sessions, keys):
            response = session.delete("/v1/api-keys/" + key)
            response.raise_for_status()
        for session in sessions: session.close()


if __name__ == "__main__":
    raise SystemExit(main())
