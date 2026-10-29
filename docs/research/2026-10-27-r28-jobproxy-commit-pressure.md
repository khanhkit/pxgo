# PxGo R28 — Real proxy child under enforced Windows Job Object while holding 2,560 CONNECTs

Date: 2026-10-27 ICT. Parent R27 immutable PR#103 `da9b5bd48f3b02dc297b505e49a8e35668dc8835`; PR#102 base `fb2aa923a753309dae515fc8be38112f45fbddc8`. Proposed TEST-ONLY gate in PR#103 DRAFT, no release.

## Gap addressed
R16 captured native isolated PXGO child-PID PrivateBytes/handles while handling 2,560 real held bidirectional CONNECT and 600 short HTTP, but did not enforce a process commit quota. R26 enforced Windows Job Object baseline+128MiB in a tiny standalone child, denying 256MiB and accepting 4MiB, but did NOT run the real proxy under quota.

R28 combines the **real PxGo Go test-child proxy** with its own native Job Object process-commit cap, containing ONLY the child PID. Parent test remains load generator and echo/HTTP origin, never in Job. It uses a bounded **baseline PrivateBytes + 384MiB** hard commit limit, informed by but not proved universally safe from R16 observed peak private commit around 302–316MiB on native Windows. It verifies JobObject Set/Query exact PROCESS_MEMORY flag+cap and assignment before CONNECT ramp. No pagefile, host/runner memory, Docker, port pool or production config is modified.

## Acceptance invariants
1. Strict opt-in PXGO_CAPACITY_JOB_PROXY_2560=1 plus experimental AUTO and classes ON, native windows-latest only, exact SHA checkout, separate process PID and real listening TCP PxGo child.
2. 2,560 *concurrent actual* CONNECT tunnels established against loopback echo origin with payload handshake; class tracker LONG==TOTAL==2560 and active physical permits exactly 2560. Generator uses at most 16 concurrent dials but maintains **2,560 simultaneously held established sockets**, not 2,560 instant SYNs.
3. 25ms native Win32 independent PID resource sampler begins before first dial, includes ramp, mixed and drain, strict recorded PID, commit and handles. Enforced hard limit must exceed observed child commit; after ramp remaining free commit budget must be less than 256MiB yet more than 4MiB.
4. Inject a deliberate OS `VirtualAlloc(MEM_COMMIT)` 256MiB *inside the live PxGo child process*, assert recognized Win32 memory-quota denial while all 2,560 held, then successful 4MiB allocation and release in the same live process. No assertion that the Go runtime itself safely recovers from arbitrary allocation failure; probe is a manual native allocation from a test-only control command.
5. Complete **600/600** short HTTP proxy requests while all long CONNECT tunnels remain live; verify all previously held echoes after artificial commit denial and short churn. No transient errors waived.
6. Close all held clients, drain observed active physical/class resources to zero, ensure bounded normal child shutdown before closing `KILL_ON_JOB_CLOSE` Job Object; never kill parent/runner. On partial dial failures, drain all goroutine results and close them before echo worker join.
7. Strict full positive log marker `JOB_PROXY_2560_RESULT enforceable=true ... held=2560 completed_short=600 denied_bytes=268435456 recovered_bytes=4194304 ... physical_after=0`, native test EXIT=0, upload logs on failure or success.

## Limitations and gate
- This is real PxGo **test executable child** (not stripped production artifact), and **synthetic 256MiB denial** under a quota chosen with headroom. It does not prove real Go heap allocator or actual admission controller can recover from organically exhausted quota, nor class-aware fairness, Windows enterprise AD/SSPI, WAN/soak, Linux PSI or global hardware safety in production.
- R27 Guardian race/coverage failure fix remains a separate immutable change; independent strict READ-ONLY KitAuditor must inspect this R28 exact-SHA source and full native raw CI, review all cleanup/marking assumptions, and validate failed-resource cleanup. Until independent verification/CI passes, R28 status TEST-STAGING, not PASS.
- Original dirty root WIP preserved. Production legacy default and DRAFT release hold unchanged.
