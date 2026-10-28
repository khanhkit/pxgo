# PxGo R34 — Fresh Windows VM isolation for exact historical socket-pressure failures

Date: 2026-10-28 ICT. Source parent R33 PR#103 immutable `c4845dfe1dcaa3c00a7cc470a02c498e9796a662`, base PR#102 `fb2aa923a753309dae515fc8be38112f45fbddc8`, both OPEN DRAFT. **Workflow+research only**; no Go runtime, tests, governor/default, host TCP/port-range, Docker or user checkout change.

## Motivation and preserved negative evidence

R31 GitHub Actions `40552653412` attempt1 native Windows mixed workload had **2/32 failed** despite 30 passing:
- R11 classes OFF seed6: `TestCapacityMixedTrafficBench/churn` failed after **448/449 attempted held CONNECT setups** due Windows bind insufficient buffer/queue full (WSAENOBUFS-like). Same VM native diagnostic **10,256 TIME_WAIT**, default dynamic range 49152–65535 (16,384 ports), after the prior short_heavy, balanced, long_heavy profiles ran in the same test process.
- fast_saturation classes ON seed1: `TestCapacityMixedCompetingAdmission` failed when **447/448** short HTTP completed, 320/320 long CONNECT established, one HTTP 502 from upstream Windows loopback bind; native snapshot **12,298 TIME_WAIT** after previous sequential profile test(s).

At SAME immutable R31 SHA, GitHub reran ONLY two failing jobs on fresh VMs; attempt2 107/107 terminal with **105 SUCCESS, 2 conditional SKIPPED**. This demonstrates variability, not proven root cause. GitHub R32 8/8 Job Object runs and R33 9/9 own-job cap query tests are separate observations and cannot erase R31 native failures.

## R34 experiment (STRICT evidence; no waivers)

Append a distinct native workflow job `windows-mixed-socket-isolation-research` with 2 scenarios × 4 independent fresh hosted Windows VMs = **8 trials**:
- `r11_off_seed6_churn`: run EXACT existing `TestCapacityMixedTrafficBench/churn`, `PXGO_CAPACITY_MIX_PROFILE=churn`, R11, class OFF, seed6; expected **449 held CONNECT, 1,404 short HTTP** from 117% workload factor. Validate positive `MIXED_RESULT profile=churn seed=6 held_long_connect=449 ... short_attempted=1404 short_completed=1404`. Go test cannot PASS merely by being skipped; strict marker required.
- `fast_on_seed1_competing`: run EXACT existing `TestCapacityMixedCompetingAdmission`, fast_saturation, class ON, seed1; must match `MIXED_COMPETE_RESULT seed=1 long_attempted=320 long_admitted=320 short_attempted=448 short_completed=448` plus Go exit0.

Every trial checks exact GitHub pull-request HEAD checkout, no prior high-socket load on its own clean VM, reads native TCP dynamic port range and Get-NetTCPConnection state counts **before AND after**, optional read-only recent Windows System event IDs 4227/4231, and uploads SHA+scenario+trial qualified raw log+diagnostic artifact with `if: always()`, including failure. Every underlying test failure is a HARD FAIL, no retry-loop or environment tuning hidden. The test body, actual offered demand, concurrency and response correctness gates remain UNCHANGED. No direct short/long causal policy comparison is supported by this intentionally nonpaired 2-scenario study.

## Preflight guard / prevented failure

The first locally generated YAML staging `ci-r34.yml` was malformed due **JavaScript replacement string interpreting literal `$'` in a PowerShell regular expression as the suffix of original workflow**, leading to a duplicated/truncated workflow. This bug was caught BEFORE ANY GitHub source mutation. It was rebuilt from exact known-good R33 workflow Git blob `74d4eb710fab55b442ba1a1c204aca217aa1b15f` via Python literal `str.replace` with 4,773-byte separately staged job; complete new YAML 51,830 bytes. Real independent PyYAML parser PASS, **15 static jobs**, all 14 original job keys retained, exactly 8 new matrix cells. The prevented file error is local research history, not a GitHub Actions failure or release.

## Interpretation / release gates

8/8 success would be evidence that exact failing workloads often work on fresh runner without *earlier* high-churn workload, consistent with TIME_WAIT and Win32 socket resource history as a confound; it would NOT prove any specific WinSock failure mechanism nor fix/replace R31 original failure. Any failed fresh-run cell is evidence that the issue can occur without prior heavy profiles. Do not adjust OS system TCP ranges, TIME_WAIT, process memory limit or suppress errors as a fix. Separate strict READ-ONLY KitAuditor source+CI exact immutable SHA verdict still REQUIRED, plus protected enterprise AD/SSPI, actual class-fair scheduler, natural Go heap pressure, WAN/soak, Linux PSI. Both PRs remain DRAFT. No merge/tag/release/default promotion.
