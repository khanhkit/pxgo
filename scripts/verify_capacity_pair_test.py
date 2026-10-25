"""Negative tests for native Windows paired-mixed benchmark evidence validator."""
import importlib.util
import sys
import unittest
from pathlib import Path

path = Path(__file__).with_name("verify-capacity-pair.py")
spec = importlib.util.spec_from_file_location("paired_validator", path)
module = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = module
spec.loader.exec_module(module)


def log(policy="r11", *, profile="balanced", seed=0, classes="on"):
    return ("=== RUN   TestCapacityMixedTrafficBench\n"
            " capacity_mixed_benchmark_test.go:309: MIXED_RESULT "
            f"profile={profile} seed={seed} "
            "held_long_connect=512 held_short=128 promoted_short_to_long=32 "
            "short_attempted=600 short_completed=600 "
            "short_p50=12ms short_p95=125ms short_p99=200ms "
            "short_offered_p95=160ms short_offered_p99=240ms short_rps=3100.25 "
            "class_current_debt=3 class_overflow_events=5 "
            "physical_active=672 heap_shared_bytes=2592000 goroutines_shared=900 "
            f"duration=1.5s policy={policy} classes={'1' if classes == 'on' else '0'}\n"
            "--- PASS: TestCapacityMixedTrafficBench (1.50s)\nPASS\n")


class StrictEvidenceTests(unittest.TestCase):
    def verify(self, r11=None, fast=None, profile="balanced", seed=0, classes="on",
               order="r11_first", order_log=None):
        manifest = (f"PAIR_POLICY profile={profile} class={classes} seed={seed} policy=r11 timewait_before=100\n"
                    f"PAIR_POLICY profile={profile} class={classes} seed={seed} policy=fast_saturation timewait_before=200\n")
        return module.verify(log("r11") if r11 is None else r11,
                             log("fast_saturation") if fast is None else fast,
                             profile=profile, seed=seed, classes=classes, order=order,
                             order_log=manifest if order_log is None else order_log)

    def test_reversed_matched_order_passes(self):
        manifest = ("PAIR_POLICY profile=balanced class=on seed=0 policy=fast_saturation timewait_before=18\n"
                    "PAIR_POLICY profile=balanced class=on seed=0 policy=r11 timewait_before=24\n")
        self.assertEqual(self.verify(order="fast_first", order_log=manifest)["expected_order"], ["fast_saturation", "r11"])

    def test_wrong_order_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(order="fast_first")

    def test_missing_timewait_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(order_log="PAIR_POLICY profile=balanced class=on seed=0 policy=r11\n")

    def test_matching_pair_passes(self):
        evidence = self.verify()
        self.assertEqual(evidence["r11"]["short_attempted"], 600)
        self.assertEqual(evidence["fast_minus_r11_offered_p95_s"], 0)

    def test_valid_off_class_passes(self):
        self.assertEqual(self.verify(log("r11", classes="off"),
                                     log("fast_saturation", classes="off"), classes="off")["classes"], "off")

    def test_missing_row_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(r11="PASS\n")

    def test_duplicate_row_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(r11=log() + log())

    def test_wrong_profile_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(r11=log(profile="churn"))

    def test_wrong_seed_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(fast=log("fast_saturation", seed=1))

    def test_wrong_policy_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(fast=log("r11"))

    def test_wrong_class_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(r11=log(classes="off"))

    def test_missing_p99_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(r11=log().replace("short_offered_p99=240ms ", ""))

    def test_duplicate_metric_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(r11=log().replace("short_rps=3100.25", "short_rps=3100.25 short_rps=1"))

    def test_short_completion_failure_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(fast=log("fast_saturation").replace("short_completed=600", "short_completed=599"))

    def test_wrong_active_count_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(fast=log("fast_saturation").replace("physical_active=672", "physical_active=671"))

    def test_zero_work_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(fast=log("fast_saturation").replace("short_attempted=600 short_completed=600", "short_attempted=0 short_completed=0"))

    def test_malformed_rps_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(r11=log().replace("short_rps=3100.25", "short_rps=nan"))

    def test_compound_go_duration_is_accepted(self):
        self.assertAlmostEqual(module.seconds("1m2.5s"), 62.5)

    def test_malformed_latency_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(r11=log().replace("short_offered_p95=160ms", "short_offered_p95=wat"))

    def test_missing_pass_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(fast=log("fast_saturation").replace("PASS\n", ""))

    def test_forged_failure_fails(self):
        with self.assertRaises(module.InvalidEvidence): self.verify(r11=log() + "FAIL\n")

    def test_unequal_paired_workload_fails(self):
        f = log("fast_saturation").replace("held_long_connect=512", "held_long_connect=513").replace("physical_active=672", "physical_active=673")
        with self.assertRaises(module.InvalidEvidence): self.verify(fast=f)


if __name__ == "__main__":
    unittest.main()
