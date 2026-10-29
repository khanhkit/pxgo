#!/usr/bin/env python3
"""Fail-closed comparison of exactly two native Windows mixed benchmark logs.

No external dependencies. Paired VM profile/seed/class and workload counts must
match, and each raw log must contain precisely one complete MIXED_RESULT row.
This validates the evidence only; it cannot establish causality under TIME_WAIT.
"""
from __future__ import annotations

import argparse
import json
import math
import re
from pathlib import Path

INT_KEYS = (
    "held_long_connect", "held_short", "promoted_short_to_long",
    "short_attempted", "short_completed", "class_current_debt",
    "class_overflow_events", "physical_active", "heap_shared_bytes",
    "goroutines_shared",
)
TIME_KEYS = ("short_p50", "short_p95", "short_p99", "short_offered_p95", "short_offered_p99", "duration")
# The native Go t.Logf at capacity_mixed_benchmark_test.go emits exactly
# these fields in this order. A substring token scanner can accept malformed
# evidence by ignoring garbage, broken delimiters and appended fragments.
# Match the COMPLETE metric row so every byte belongs to a known field.
METRIC_FIELDS = (
    "profile", "seed", "held_long_connect", "held_short", "promoted_short_to_long",
    "short_attempted", "short_completed", "short_p50", "short_p95", "short_p99",
    "short_offered_p95", "short_offered_p99", "short_rps", "class_current_debt",
    "class_overflow_events", "physical_active", "heap_shared_bytes", "goroutines_shared",
    "duration", "policy", "classes",
)
METRIC_ROW = re.compile(r"^" + " ".join(rf"{field}=(?P<{field}>\S+)" for field in METRIC_FIELDS) + r"$")
# Consume only a native Go testing.T Logf record from this exact test file.
# Other output can mention MIXED_RESULT, but must not impersonate evidence.
METRIC = re.compile(
    r"(?m)^[ \t]*capacity_mixed_benchmark_test\.go:[0-9]+:[ \t]+MIXED_RESULT[ \t]+([^\r\n]+)\r?$"
)
DURATION_PART = re.compile(r"(\d+(?:\.\d+)?)(ns|µs|us|ms|s|m|h)")
# PowerShell emits exactly these five fields in this order. Requiring an
# entire native record (not substring token scanning) rejects unparsed text and
# ambiguous fields even when every expected key is also present.
ORDER_ROW = re.compile(
    r"^PAIR_POLICY profile=(?P<profile>\S+) class=(?P<class>on|off) "
    r"seed=(?P<seed>[0-9]+) policy=(?P<policy>r11|fast_saturation) "
    r"timewait_before=(?P<timewait_before>-?[0-9]+)$"
)


class InvalidEvidence(ValueError):
    pass


def seconds(s: str) -> float:
    # Go Duration.String can emit compound values such as 1m2.5s.
    units = {"ns": 1e-9, "µs": 1e-6, "us": 1e-6, "ms": 1e-3, "s": 1.0, "m": 60.0, "h": 3600.0}
    total = 0.0
    pos = 0
    for m in DURATION_PART.finditer(s):
        if m.start() != pos:
            raise InvalidEvidence(f"not a Go duration: {s!r}")
        total += float(m[1]) * units[m[2]]
        pos = m.end()
    if pos != len(s) or not s:
        raise InvalidEvidence(f"not a Go duration: {s!r}")
    return total


def parse_log(raw: str, *, profile: str, seed: int, classes: str, policy: str) -> dict:
    rows = METRIC.findall(raw)
    if len(rows) != 1:
        raise InvalidEvidence(f"{policy} has {len(rows)} MIXED_RESULT rows, expected exactly 1")
    row = rows[0]
    match = METRIC_ROW.fullmatch(row)
    if match is None:
        raise InvalidEvidence(f"{policy} malformed full native MIXED_RESULT row: {row!r}")
    fields = match.groupdict()
    for key, expected in (("profile", profile), ("seed", str(seed)), ("policy", policy), ("classes", "1" if classes == "on" else "0")):
        if fields[key] != expected:
            raise InvalidEvidence(f"{policy} {key}={fields[key]!r}, expected {expected!r}")
    parsed = {key: fields[key] for key in ("profile", "seed", "policy", "classes")}
    for key in INT_KEYS:
        try:
            v = int(fields[key])
        except ValueError as exc:
            raise InvalidEvidence(f"{policy} invalid integer {key}={fields[key]}") from exc
        if v < 0:
            raise InvalidEvidence(f"{policy} negative {key}={v}")
        parsed[key] = v
    for key in TIME_KEYS:
        v = seconds(fields[key])
        if not math.isfinite(v) or v <= 0:
            raise InvalidEvidence(f"{policy} invalid {key}={fields[key]}")
        parsed[key + "_s"] = v
    try:
        rps = float(fields["short_rps"])
    except ValueError as exc:
        raise InvalidEvidence(f"{policy} invalid short_rps") from exc
    if not math.isfinite(rps) or rps <= 0:
        raise InvalidEvidence(f"{policy} invalid RPS {rps}")
    parsed["short_rps"] = rps
    # These sample percentiles are derived from the same set of successful
    # HTTP exchanges. A nonsensical ranking is corrupt evidence even when
    # every individual duration is syntactically valid.
    if not (parsed["short_p50_s"] <= parsed["short_p95_s"] <= parsed["short_p99_s"]):
        raise InvalidEvidence(f"{policy} short latency percentile order inconsistent")
    if parsed["short_offered_p95_s"] > parsed["short_offered_p99_s"]:
        raise InvalidEvidence(f"{policy} offered latency percentile order inconsistent")
    # For each individual request, offered-time >= after-client-token service
    # time. Hence their ordered empirical percentiles must obey this bound.
    if parsed["short_offered_p95_s"] < parsed["short_p95_s"]:
        raise InvalidEvidence(f"{policy} offered p95 less than measured service p95")
    if parsed["short_offered_p99_s"] < parsed["short_p99_s"]:
        raise InvalidEvidence(f"{policy} offered p99 less than measured service p99")
    if min(parsed["held_long_connect"], parsed["held_short"], parsed["promoted_short_to_long"], parsed["short_attempted"]) <= 0:
        raise InvalidEvidence(f"{policy} empty offered workload")
    if parsed["short_attempted"] != parsed["short_completed"]:
        raise InvalidEvidence(f"{policy} short completion mismatch")
    expected_active = parsed["held_long_connect"] + parsed["held_short"] + parsed["promoted_short_to_long"]
    if parsed["physical_active"] != expected_active:
        raise InvalidEvidence(f"{policy} mismatched active physical permit count")
    if "FAIL" in raw or not re.search(r"(?m)^PASS\s*$", raw):
        raise InvalidEvidence(f"{policy} missing unambiguous native Go test PASS")
    return parsed


def verify_execution_order(raw: str, *, profile: str, seed: int, classes: str, order: str) -> dict:
    expected = ["r11", "fast_saturation"] if order == "r11_first" else ["fast_saturation", "r11"]
    rows = raw.splitlines()  # Handles PowerShell CRLF without relaxing record syntax.
    if len(rows) != 2:
        raise InvalidEvidence(f"expected exactly 2 native PAIR_POLICY rows, got {len(rows)}")
    observed = []
    for row in rows:
        match = ORDER_ROW.fullmatch(row)
        if match is None:
            raise InvalidEvidence(f"malformed native PAIR_POLICY row: {row!r}")
        fields = match.groupdict()
        for key, value in (("profile", profile), ("class", classes), ("seed", str(seed))):
            if fields[key] != value:
                raise InvalidEvidence(f"native order {key}={fields[key]!r} expected {value!r}")
        count = int(fields["timewait_before"])
        # The workflow emits nonnegative counts, or -1 if the native Windows
        # sensor itself is unavailable. Other negative counts cannot be real.
        if count < -1:
            raise InvalidEvidence(f"impossible native TIME_WAIT baseline {count}")
        observed.append({"policy": fields["policy"], "timewait_before": count})
    actual_order = [row["policy"] for row in observed]
    if actual_order != expected:
        raise InvalidEvidence(f"native policy order {actual_order}, expected {expected}")
    return {"observed_order": actual_order, "before_each_policy": observed,
            "timewait_measurement_available": all(x["timewait_before"] >= 0 for x in observed)}


def verify(r11_raw: str, fast_raw: str, *, profile: str, seed: int, classes: str,
           order: str, order_log: str) -> dict:
    if classes not in ("on", "off"):
        raise InvalidEvidence(f"invalid class mode {classes!r}")
    if profile not in ("short_heavy", "balanced", "long_heavy", "churn"):
        raise InvalidEvidence(f"invalid profile {profile!r}")
    if seed < 0 or seed > 7:
        raise InvalidEvidence(f"invalid seed {seed}")
    if order not in ("r11_first", "fast_first"):
        raise InvalidEvidence(f"invalid paired execution order {order!r}")
    execution = verify_execution_order(order_log, profile=profile, seed=seed,
                                       classes=classes, order=order)
    r11 = parse_log(r11_raw, profile=profile, seed=seed, classes=classes, policy="r11")
    fast = parse_log(fast_raw, profile=profile, seed=seed, classes=classes, policy="fast_saturation")
    workload_fields = ("held_long_connect", "held_short", "promoted_short_to_long", "short_attempted")
    differences = [key for key in workload_fields if r11[key] != fast[key]]
    if differences:
        raise InvalidEvidence(f"paired workloads differ in {differences}")
    return {
        "profile": profile, "seed": seed, "classes": classes,
        "expected_order": execution["observed_order"],
        "actual_execution": execution,
        "r11": r11, "fast_saturation": fast,
        "fast_minus_r11_offered_p95_s": fast["short_offered_p95_s"] - r11["short_offered_p95_s"],
        "fast_minus_r11_offered_p99_s": fast["short_offered_p99_s"] - r11["short_offered_p99_s"],
        "fast_minus_r11_short_rps": fast["short_rps"] - r11["short_rps"],
        "claim": "matched same-VM descriptive pair; does not eliminate TIME_WAIT order effects",
    }


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--profile", required=True)
    p.add_argument("--seed", required=True, type=int)
    p.add_argument("--classes", required=True)
    p.add_argument("--order", required=True)
    p.add_argument("--order-log", required=True, type=Path)
    p.add_argument("--r11", required=True, type=Path)
    p.add_argument("--fast", required=True, type=Path)
    p.add_argument("--output", required=True, type=Path)
    a = p.parse_args()
    try:
        result = verify(a.r11.read_text(encoding="utf-8", errors="replace"),
                        a.fast.read_text(encoding="utf-8", errors="replace"),
                        profile=a.profile, seed=a.seed, classes=a.classes,
                        order=a.order, order_log=a.order_log.read_text(encoding="utf-8", errors="replace"))
    except (InvalidEvidence, OSError) as exc:
        print(f"PAIR_EVIDENCE_INVALID: {exc}")
        return 1
    a.output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"PAIR_EVIDENCE_VALID profile={a.profile} seed={a.seed} classes={a.classes} "
          f"fast_minus_r11_offered_p95_s={result['fast_minus_r11_offered_p95_s']:.6f}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
