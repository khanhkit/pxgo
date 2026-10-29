#!/usr/bin/env python3
"""Reproducible descriptive order-balanced analysis of immutable Windows R20 pairs.

The two execution orders run on DIFFERENT Windows VMs. This is not a causal
crossover estimate and bootstrap intervals do NOT correct host/order biases.
No dependencies outside Python stdlib.
"""
from __future__ import annotations

import argparse
import csv
import hashlib
import json
import math
import random
import re
import statistics
from collections import defaultdict
from pathlib import Path

PROFILES = ("short_heavy", "balanced", "long_heavy", "churn")
CLASSES = ("off", "on")
SEEDS = ("0", "1")
ORDERS = ("r11_first", "fast_first")
COLUMNS = (
    "job_id", "job_url", "source_sha", "profile", "classes", "seed", "order",
    "r11_offered_p95_ms", "fast_offered_p95_ms", "delta_fast_minus_r11_p95_ms",
    "delta_p99_ms", "delta_rps", "short_requested",
)
RUN_ID = "40220215522"
# Hash of the previously immutable-GitHub-job-derived R20 replay CSV at the
# independently reviewed R21 HEAD. Protects against accidental CSV drift;
# this is NOT independent authentication of the underlying Windows job logs.
PINNED_R20_CSV_SHA256 = "82b0a1c9c48a315c6ad6e0a60dfe3df15702f0737cedfec1f9f21ffc179e8386"
TOLERANCE_MS = 0.0002  # source metrics rounded at most four decimal places


class InvalidEvidence(ValueError):
    """Fail closed on incomplete, ambiguous, fabricated or inconsistent evidence."""


def validate_pinned_csv(path: Path) -> None:
    try:
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
    except OSError as exc:
        raise InvalidEvidence(f"unable to read pinned R20 CSV: {exc}") from exc
    if digest != PINNED_R20_CSV_SHA256:
        raise InvalidEvidence(f"pinned R20 CSV digest mismatch: {digest}")


def finite_number(row: dict, field: str) -> float:
    try:
        number = float(row[field])
    except (KeyError, ValueError, TypeError) as exc:
        raise InvalidEvidence(f"invalid {field}: {row.get(field)!r}") from exc
    if not math.isfinite(number):
        raise InvalidEvidence(f"nonfinite {field}: {row[field]!r}")
    return number


def percentile(sorted_data: list[float], portion: float) -> float:
    return sorted_data[max(0, min(len(sorted_data) - 1, int((len(sorted_data) - 1) * portion)))]


def bootstrap(values: list[float], *, repetitions: int = 10000) -> dict:
    # Preserve the four distinct workload families by resampling one of their
    # four (class x seed) strata at each draw, avoiding accidental replacement
    # of long-heavy experiments with churn during the uncertainty calculation.
    if len(values) != 16:
        raise InvalidEvidence(f"bootstrap requires all 16 balanced strata, got {len(values)}")
    rng = random.Random(20261027)
    estimates = []
    for _ in range(repetitions):
        picked = [values[4 * family + rng.randrange(4)] for family in range(4) for _ in range(4)]
        estimates.append(statistics.mean(picked))
    estimates.sort()
    return {"method": "conditional descriptive percentile-bootstrap sensitivity, profile-stratified; not causal", "samples": repetitions,
            "mean_95pct_interval_ms": [round(percentile(estimates, 0.025), 4),
                                       round(percentile(estimates, 0.975), 4)]}


def analyze(rows: list[dict], *, expected_sha: str) -> dict:
    if not re.fullmatch(r"[0-9a-f]{40}", expected_sha):
        raise InvalidEvidence("required immutable source SHA is not a full Git SHA")
    groups = defaultdict(dict)
    jobs = set()
    for row in rows:
        if set(row) != set(COLUMNS):
            raise InvalidEvidence(f"unexpected or missing CSV columns: {set(row) ^ set(COLUMNS)}")
        key = (row["profile"], row["classes"], row["seed"])
        order = row["order"]
        if key[0] not in PROFILES or key[1] not in CLASSES or key[2] not in SEEDS or order not in ORDERS:
            raise InvalidEvidence(f"unexpected cohort or policy order: {key}, {order}")
        if row["source_sha"] != expected_sha:
            raise InvalidEvidence("mixed or unexpected immutable benchmark SHA")
        job = row["job_id"]
        if not re.fullmatch(r"[1-9][0-9]*", job) or job in jobs:
            raise InvalidEvidence(f"duplicate or noncanonical GitHub job ID: {job!r}")
        jobs.add(job)
        if row["job_url"] != f"https://github.com/khanhkit/pxgo/actions/runs/{RUN_ID}/job/{job}":
            raise InvalidEvidence(f"job URL not anchored to R20 run and matching ID: {job!r}")
        try:
            requests = int(row["short_requested"])
        except ValueError as exc:
            raise InvalidEvidence("invalid offered request count") from exc
        if requests <= 0 or str(requests) != row["short_requested"]:
            raise InvalidEvidence("invalid or noncanonical short request count")
        r11, fast, diff = [finite_number(row, field) for field in
                           ("r11_offered_p95_ms", "fast_offered_p95_ms", "delta_fast_minus_r11_p95_ms")]
        if min(r11, fast) <= 0 or abs(fast - r11 - diff) > TOLERANCE_MS:
            raise InvalidEvidence(f"inconsistent p95 policy observations: {key}, {order}")
        if not all(math.isfinite(finite_number(row, metric)) for metric in ("delta_p99_ms", "delta_rps")):
            raise InvalidEvidence("nonfinite secondary metrics")
        if order in groups[key]:
            raise InvalidEvidence(f"duplicate order in cohort: {key}, {order}")
        groups[key][order] = {"job_id": job, "delta_ms": diff, "short_requested": requests}
    expected = {(p, c, s) for p in PROFILES for c in CLASSES for s in SEEDS}
    if set(groups) != expected or any(set(pair) != set(ORDERS) for pair in groups.values()):
        raise InvalidEvidence("not exactly 16 complete balanced cohorts with both execution orders")
    balanced, order_effect, details = [], [], []
    # Stable sort grouped by workload profile for stratified-resampling validity.
    for profile in PROFILES:
        for classes in CLASSES:
            for seed in SEEDS:
                a = groups[(profile, classes, seed)]["r11_first"]
                b = groups[(profile, classes, seed)]["fast_first"]
                if a["short_requested"] != b["short_requested"]:
                    raise InvalidEvidence(f"offered cohort differs between execution orders: {profile}, {classes}, {seed}")
                symmetric = (a["delta_ms"] + b["delta_ms"]) / 2
                contrast = (a["delta_ms"] - b["delta_ms"]) / 2
                balanced.append(symmetric)
                order_effect.append(contrast)
                details.append({"profile": profile, "classes": classes, "seed": int(seed),
                                "r11_first_job_id": a["job_id"], "fast_first_job_id": b["job_id"],
                                "offered_requests": a["short_requested"],
                                "order_balanced_delta_ms": round(symmetric, 4),
                                "half_order_contrast_ms": round(contrast, 4)})
    all_deltas = [r["delta_ms"] for pair in groups.values() for r in pair.values()]
    return {
        "provenance": {"source_sha": expected_sha, "run_id": RUN_ID,
                       "raw_rows": len(rows), "unique_job_ids": len(jobs),
                       "pinned_r20_csv_sha256": PINNED_R20_CSV_SHA256,
                       "csv_digest_scope": "tamper-evident local replay only; native Windows raw-log provenance independently audited at R21",
                       "complete_profile_class_seed_strata": len(groups)},
        "interpretation": "observational paired cross-host order balancing; NOT an identifiable causal policy effect",
        "fast_minus_r11_delta_positive_means": "lower HTTP offered p95 for R11",
        "original_unadjusted_pair_median_ms": round(statistics.median(all_deltas), 4),
        "balanced_stratum_median_ms": round(statistics.median(balanced), 4),
        "balanced_stratum_mean_ms": round(statistics.mean(balanced), 4),
        "balanced_strata_favor_r11": sum(x > 0 for x in balanced),
        "balanced_strata_favor_fast": sum(x < 0 for x in balanced),
        "mean_half_order_contrast_ms": round(statistics.mean(order_effect), 4),
        "median_half_order_contrast_ms": round(statistics.median(order_effect), 4),
        "uncertainty": bootstrap(balanced),
        "limitations": ["Execution orders run on different GitHub-hosted Windows VMs",
                        "Same-VM first-versus-second trial inherits TIME_WAIT and port-state carryover",
                        "Only two seeds per profile and class; 16 order-balanced strata",
                        "Descriptive percentile-bootstrap sensitivity is conditional on the observed strata/resampling scheme, not a production confidence interval; it cannot remove VM, order, temporal or measurement bias",
                        "R20 raw provenance independently validated by strict R21 parser; this script validates the pinned derived CSV, not remote raw job logs"],
        "strata": details,
    }


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--input", required=True, type=Path)
    p.add_argument("--expected-sha", required=True)
    p.add_argument("--output", required=True, type=Path)
    args = p.parse_args()
    try:
        validate_pinned_csv(args.input)
        with args.input.open(encoding="utf-8", newline="") as f:
            reader = csv.DictReader(f)
            if reader.fieldnames != list(COLUMNS):
                raise InvalidEvidence("wrong ordered CSV header or missing columns")
            result = analyze(list(reader), expected_sha=args.expected_sha)
        args.output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    except (InvalidEvidence, OSError) as exc:
        print(f"CROSSOVER_EVIDENCE_INVALID: {exc}")
        return 1
    print(f"CROSSOVER_EVIDENCE_VALID complete_strata={result['provenance']['complete_profile_class_seed_strata']} "
          f"balanced_delta_ms={result['balanced_stratum_median_ms']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
