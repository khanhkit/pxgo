# PxGo R35 — Child-only nested Windows Job Object ancestor committed-memory counterexample

Date 2026-10-28 ICT. Experimental PR#103 candidate parent R34 immutable `6fec954050f7297034ffa9b00ba686d6011990a3`, base PR#102 `fb2aa923a753309dae515fc8be38112f45fbddc8`. This round adds ONLY a separate **test-only Windows Go file**, ONE gated native CI workflow job and this research document. No PxGo runtime/default changes, OS-wide memory/system TCP/port settings, Docker changes, branch merge/tag/release.

## Why needed

R33 native `QueryInformationJobObject(NULL, ExtendedLimitInformation)` from inside a real PxGo test-child successfully returned the SAME immediate process committed-memory limit as the parent-owned Job Object across 9/9 native runs. It was mistakenly tempting to extrapolate that into a general production Windows hard-memory sensor. Microsoft documents the NULL handle selects the immediately associated job, but *effective* process commitment limits can be the most restrictive of **all ancestors in the nested job chain**:
- https://learn.microsoft.com/en-us/windows/win32/api/jobapi2/nf-jobapi2-queryinformationjobobject
- https://learn.microsoft.com/en-us/windows/win32/procthread/nested-jobs
- https://learn.microsoft.com/en-us/windows/win32/api/jobapi2/nf-jobapi2-assignprocesstojobobject

Thus reading only the immediate job can falsely overstate usable memory. A production sensor must not use immediate job cap alone as a universally authoritative envelope.

## R35 minimal native falsifiable experiment

1. Launch a NEW separate nested helper `TestCapacityWindowsNestedJobChild` under opt-in env, command with bounded context and real executable PID; CI parent, host OS, other test/proxy processes are never restricted.
2. Parent samples helper's native `PrivateBytes` baseline, creates root and leaf Job Objects, sets ROOT per-process committed-memory cap = baseline+96MiB and LEAF = baseline+192MiB (strictly greater), readbacks from each owned Job Object ensure flags/limits/struct sizes before assigning.
3. Assign helper PID explicitly ROOT then LEAF, which establishes nested hierarchy; fail test if Windows refuses nesting, never SKIP or weaken.
4. Only after assignment send private `RUN` command to helper. Helper native NULL-job query must return LEAF's +192MiB process committed-memory cap and memory limit flag (not ROOT's stricter cap). Parent exact-matches child immediate self-query with leaf.
5. Inside same still-live helper perform Win32 `VirtualAlloc MEM_COMMIT|MEM_RESERVE` 128MiB: it fits the observed LEAF +192MiB but exceeds ROOT +96MiB, so it MUST fail with a recognized Windows quota/insufficient-memory errno (no arbitrary error accepted); then separate 4MiB MEM_COMMIT must SUCCEED and be freed. Verify helper `PrivateBytes` remains at/below ROOT cap. Parent sends `QUIT`, waits for clean exit, owned handles closed AFTER wait.
6. Emit strict positive `NESTED_JOB_RESULT enforced_root_stricter=true pid=... baseline_private=... ancestor_limit=... immediate_query_limit=... denied_commit=134217728 recovered_commit=4194304 after_private=...`. Native job checks exact GitHub SHA, actual Go test exit, marker, uploaded raw Windows logs on success or failure.

This is a counterexample test of immediate-job query sufficiency, not an attempt to organically exhaust Go runtime. All denied allocations are deliberate Win32 probes in helper, not natural Go heap pressure. It does not prove PxGo CPU/fairness/port safety or whether a particular GitHub Actions worker always has nested ancestors; test itself constructs chain. If test cannot establish two nested jobs, report FAIL and investigate actual Win32 API restrictions rather than waiving.

## Local tests and safeguards

- Whole Windows amd64 crosscompile and `go vet`, `gofmt -l` all PASS in disposable isolated checkout of exact R34 parent; no original root WIP touched.
- Full GitHub Actions workflow built from valid R34 Git blob `022608ab72df2fa67ac7c4af0e5be195df9b7768` using literal Python insertion (not JavaScript replacement syntax involving `$'`), real PyYAML parse PASS with 16 static jobs (15 previous keys preserved) and one new strict 4-step native job.
- GitHub native R35 exact-SHA CI and independent KitAuditor verdict remain PENDING until new immutable commit and full raw evidence.
- Protected corporate AD/SSPI, natural Go allocator/committed memory failure, real class-fair admission, WAN/soak, Linux PSI remain blockers. No merge/release; PRs DRAFT.
