"""Adversarial fixtures for pinned R20 native Windows order-balanced evidence."""
from __future__ import annotations

import copy
import csv
import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).parent
SPEC = importlib.util.spec_from_file_location("crossover_analyzer", HERE / "analyze-capacity-crossover.py")
mod = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = mod
SPEC.loader.exec_module(mod)
SHA = "22d05d66d06538c1e00cd4b6d9474be6a36c7a49"
DATA = HERE.parent / "docs/research/2026-10-26-r20-native-windows-paired-replay.csv"


class CrossoverEvidenceTests(unittest.TestCase):
    def setUp(self):
        with DATA.open(encoding="utf-8", newline="") as f:
            reader = csv.DictReader(f)
            self.assertEqual(reader.fieldnames, list(mod.COLUMNS))
            self.rows = list(reader)

    def assert_reject(self, mutate):
        rows = copy.deepcopy(self.rows)
        mutate(rows)
        with self.assertRaises(mod.InvalidEvidence):
            mod.analyze(rows, expected_sha=SHA)

    def test_full_immutable_native_cohorts(self):
        output = mod.analyze(self.rows, expected_sha=SHA)
        self.assertEqual(output["provenance"]["raw_rows"], 32)
        self.assertEqual(output["provenance"]["complete_profile_class_seed_strata"], 16)
        self.assertEqual(output["balanced_strata_favor_r11"], 13)
        self.assertAlmostEqual(output["balanced_stratum_median_ms"], 11.0637)
        self.assertEqual(output["uncertainty"]["samples"], 10000)

    def test_pinned_r20_csv_matches_reviewed_baseline(self):
        mod.validate_pinned_csv(DATA)

    def test_mutated_native_csv_is_rejected_by_cli_digest_gate(self):
        with tempfile.TemporaryDirectory() as temp:
            invalid = Path(temp) / "modified.csv"
            invalid.write_bytes(DATA.read_bytes().replace(b"short_heavy", b"long_heavy", 1))
            with self.assertRaises(mod.InvalidEvidence):
                mod.validate_pinned_csv(invalid)

    def test_input_order_does_not_change_output(self):
        original = mod.analyze(self.rows, expected_sha=SHA)
        self.assertEqual(original, mod.analyze(list(reversed(self.rows)), expected_sha=SHA))

    def test_same_order_duplicate_rejected(self):
        self.assert_reject(lambda rows: rows.append(copy.deepcopy(rows[0])))

    def test_missing_order_rejected(self):
        self.assert_reject(lambda rows: rows.pop(0))

    def test_unexpected_extra_cohort_rejected(self):
        self.assert_reject(lambda rows: rows[0].__setitem__("seed", "2"))

    def test_unknown_profile_rejected(self):
        self.assert_reject(lambda rows: rows[0].__setitem__("profile", "all"))

    def test_unknown_class_rejected(self):
        self.assert_reject(lambda rows: rows[0].__setitem__("classes", "none"))

    def test_wrong_policy_order_rejected(self):
        self.assert_reject(lambda rows: rows[0].__setitem__("order", "middle"))

    def test_mixed_source_sha_rejected(self):
        self.assert_reject(lambda rows: rows[0].__setitem__("source_sha", "f" * 40))

    def test_duplicate_job_id_rejected(self):
        self.assert_reject(lambda rows: rows[0].__setitem__("job_id", rows[1]["job_id"]))

    def test_wrong_job_link_rejected(self):
        self.assert_reject(lambda rows: rows[0].__setitem__("job_url", "https://github.com/khanhkit/pxgo/actions/runs/42/job/1"))

    def test_fabricated_delta_rejected(self):
        self.assert_reject(lambda rows: rows[0].__setitem__("delta_fast_minus_r11_p95_ms", "999"))

    def test_nan_and_inf_rejected(self):
        for column in ("r11_offered_p95_ms", "delta_p99_ms", "delta_rps"):
            for val in ("NaN", "Infinity", "-Infinity"):
                with self.subTest(column=column, val=val):
                    self.assert_reject(lambda rows: rows[0].__setitem__(column, val))

    def test_zero_or_negative_duration_rejected(self):
        self.assert_reject(lambda rows: rows[0].__setitem__("fast_offered_p95_ms", "0"))

    def test_requested_count_zero_rejected(self):
        self.assert_reject(lambda rows: rows[0].__setitem__("short_requested", "0"))

    def test_requested_count_negative_rejected(self):
        self.assert_reject(lambda rows: rows[0].__setitem__("short_requested", "-480"))

    def test_differing_offered_workload_between_orders_rejected(self):
        self.assert_reject(lambda rows: rows[0].__setitem__("short_requested", "12345"))

    def test_missing_column_rejected(self):
        self.assert_reject(lambda rows: rows[0].pop("job_id"))

    def test_unknown_column_rejected(self):
        self.assert_reject(lambda rows: rows[0].__setitem__("untrusted", "garbage"))

    def test_wrong_pinned_expected_sha_rejected(self):
        with self.assertRaises(mod.InvalidEvidence):
            mod.analyze(self.rows, expected_sha="e" * 40)

    def test_profile_stratified_bootstrap_repeatable(self):
        values = list(range(16))
        self.assertEqual(mod.bootstrap(values), mod.bootstrap(values))
        with self.assertRaises(mod.InvalidEvidence):
            mod.bootstrap(values[:8])


if __name__ == "__main__":
    unittest.main()
