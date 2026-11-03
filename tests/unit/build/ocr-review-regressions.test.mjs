import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import vm from "node:vm";
import test from "node:test";
import { buildClaimStatus, isRetryableGhRead, parseWorkerInputs } from "../../../scripts/ci/github-runner-fallback.mjs";
import { summarizeRunnerSamples } from "../../../scripts/ci/summarize-runner-fallback.mjs";
import { runFallbackScheduler, waitForAttemptCompletion } from "../../../scripts/ci/runner-fallback.mjs";

test("GitHub fallback retries only GET-like API calls", () => {
  assert.equal(isRetryableGhRead(["repos/khanhkit/pxgo/actions/runs"]), true);
  for (const unsafe of ["-X", "-XPOST", "--method", "--method=POST", "-f", "-F", "--field=a=b", "--raw-field=a=b", "--input=x"]) {
    assert.equal(isRetryableGhRead(["repos/khanhkit/pxgo/statuses/main", unsafe]), false, unsafe);
  }
});

test("incomplete samples sort after valid runner latency", () => {
  const samples = [
    { runner: "incomplete", conclusion: "failure" },
    { runner: "complete", queuedAt: 10, startedAt: 15, completedAt: 18, conclusion: "success" },
  ];
  const rows = summarizeRunnerSamples(samples);
  assert.equal(rows[0].runner, "complete");
  assert.equal(rows[1].runner, "incomplete");
  assert.equal(rows[1].medianTotalSeconds, null);
});

test("worker completion polling is bounded", async () => {
  let now = 0;
  const adapter = {
    now: () => now,
    sleep: async () => { now += 1; },
    observeAttempt: async () => ({ status: "in_progress" }),
  };
  await assert.rejects(
    waitForAttemptCompletion(adapter, {}, { timeoutSeconds: 2, pollIntervalMs: 1 }),
    /exceeded its deadline/
  );
});

test("invalid worker dispatch inputs fail closed", () => {
  assert.throws(() => parseWorkerInputs(["BAD"]), /invalid worker input/);
  assert.deepEqual(parseWorkerInputs(["pr_number=10"]), { pr_number: "10" });
});


test("denied runner claim is a non-failing scheduling decision, never a grant", () => {
  assert.deepEqual(buildClaimStatus("pending", "ubuntu-24.04-arm"), {
    state: "pending",
    description: "runner claim pending: ubuntu-24.04-arm",
  });
  assert.deepEqual(buildClaimStatus("denied", "ubuntu-24.04-arm"), {
    state: "success",
    description: "runner claim denied: ubuntu-24.04-arm",
  });
  assert.deepEqual(buildClaimStatus("granted", "ubuntu-latest"), {
    state: "success",
    description: "runner claim granted: ubuntu-latest",
  });
  assert.throws(() => buildClaimStatus("unexpected", "ubuntu-latest"), /invalid runner claim/);
});


test("queue timeout is neutral-looking but cannot authorize OCR, and fallback waits for cancellation", async () => {
  const workload = {
    id: "ocr-review",
    compatibleRunners: ["ubuntu-24.04-arm", "ubuntu-latest"],
    queueTimeoutSeconds: 2,
    waitOnExhaustion: false,
  };
  function runCase(firstConclusion) {
    const events = [];
    const claims = new Map();
    let now = 0;
    return {
      events,
      claims,
      adapter: {
        now: () => now,
        sleep: async (ms) => { now += ms / 1000; },
        async dispatchAttempt({ runner, claimContext }) {
          events.push(["dispatch", runner]);
          return { runner, claimContext };
        },
        async setClaim(attempt, state) {
          claims.set(attempt.runner, { verdict: state, ...buildClaimStatus(state, attempt.runner) });
          events.push(["claim", attempt.runner, state]);
        },
        async observeAttempt(attempt) {
          if (attempt.runner !== "ubuntu-24.04-arm") return { status: "in_progress" };
          return claims.get(attempt.runner)?.verdict === "denied"
            ? { status: "completed", conclusion: firstConclusion }
            : { status: "queued" };
        },
        async cancelAttempt(attempt) {
          assert.equal(claims.get(attempt.runner)?.verdict, "denied");
          events.push(["cancel", attempt.runner]);
        },
      },
    };
  }

  const success = runCase("cancelled");
  const started = await runFallbackScheduler(success.adapter, workload, {
    claimNonce: "ocr-positive", pollIntervalMs: 1000,
  });
  assert.equal(started.status, "started");
  assert.equal(started.runner, "ubuntu-latest");
  assert.equal(started.attempts[0].outcome, "cancelled-after-timeout");
  assert.deepEqual(success.events.filter(([kind]) => kind === "dispatch").map(([, runner]) => runner),
    ["ubuntu-24.04-arm", "ubuntu-latest"]);
  assert.deepEqual(success.events.filter(([kind, , verdict]) => kind === "claim" && verdict === "granted")
    .map(([, runner]) => runner), ["ubuntu-latest"]);
  assert.ok(success.events.findIndex(([kind]) => kind === "cancel") <
    success.events.findIndex(([kind, runner]) => kind === "dispatch" && runner === "ubuntu-latest"));
  const denied = success.claims.get("ubuntu-24.04-arm");
  assert.equal(denied.state, "success");
  assert.equal(denied.description, "runner claim denied: ubuntu-24.04-arm");
  assert.notEqual(denied.description, "runner claim granted: ubuntu-24.04-arm");

  const failure = runCase("failure");
  const failed = await runFallbackScheduler(failure.adapter, workload, {
    claimNonce: "ocr-negative", pollIntervalMs: 1000,
  });
  assert.equal(failed.status, "terminal");
  assert.equal(failed.conclusion, "failure");
  assert.equal(failure.events.filter(([kind]) => kind === "dispatch").length, 1,
    "a failed cancellation must never dispatch a second worker");
});



test("actual trusted-base claim gate rejects spoofed, missing, duplicate and failed statuses", async () => {
  const workflow = await readFile(
    new URL("../../../.github/workflows/ocr-review-worker.yml", import.meta.url),
    "utf8"
  );
  const beginMarker = "// OCR_CLAIM_GATE_BEGIN";
  const endMarker = "// OCR_CLAIM_GATE_END";
  const begin = workflow.indexOf(beginMarker);
  const end = workflow.indexOf(endMarker, begin + beginMarker.length);
  assert.ok(begin > 0 && end > begin, "trusted-base claim gate markers missing");
  const script = workflow.slice(begin + beginMarker.length, end)
    .split("\n")
    .map((line) => line.startsWith("            ") ? line.slice(12) : line)
    .join("\n");

  const claimContext = "pxgo/runner-claim/ocr-review/synthetic-1/2";
  const grantDescription = "runner claim granted: ubuntu-latest";

  async function evaluateClaim(statuses, options = {}) {
    const info = [];
    const failures = [];
    const fakeDate = options.advanceClock ? {
      now: (() => { let clock = 0; return () => { clock += 60_001; return clock; }; })(),
    } : Date;
    const github = {
      rest: { repos: { listCommitStatusesForRef: async (args) => {
        assert.equal(args.ref, "0123456789abcdef");
        assert.equal(args.per_page, 100);
        if (options.apiFailure) throw Error("injected 503");
        return { data: statuses };
      } } },
    };
    await vm.runInNewContext("(async () => {\n" + script + "\n})()", {
      context: { repo: { owner: "khanhkit", repo: "pxgo" } },
      process: { env: {
        SOURCE_SHA: "0123456789abcdef",
        CLAIM_CONTEXT: claimContext,
        EXPECTED_RUNNER_LABEL: "ubuntu-latest",
      } },
      github,
      core: {
        info: (message) => info.push(message),
        setFailed: (message) => failures.push(message),
      },
      Date: fakeDate, Promise,
      setTimeout: (callback) => { callback(); },
    }, { timeout: 1000 });
    return { info, failures };
  }

  const claim = (state, description) => ({ context: claimContext, state, description });
  for (const status of [
    claim("success", "runner claim denied: ubuntu-latest"),
    claim("success", "runner claim granted: ubuntu-24.04-arm"),
    claim("failure", grantDescription),
    claim("success", grantDescription + " "),
  ]) {
    const result = await evaluateClaim([status]);
    assert.deepEqual(result.info, [], "unexpected authorization diagnostic");
    assert.deepEqual(result.failures, ["Scheduler claim not granted: " + claimContext]);
  }

  const duplicates = await evaluateClaim([
    claim("success", "runner claim denied: ubuntu-latest"),
    claim("success", grantDescription),
  ]);
  assert.equal(duplicates.info.length, 0);
  assert.equal(duplicates.failures.length, 1);

  const absent = await evaluateClaim([{ context: "other", state: "success", description: grantDescription }],
    { advanceClock: true });
  assert.deepEqual(absent.info, []);
  assert.deepEqual(absent.failures, ["Timed out waiting for scheduler claim: " + claimContext]);

  await assert.rejects(evaluateClaim([], { apiFailure: true }), /injected 503/);

  const granted = await evaluateClaim([
    { context: "some-other-runner", state: "success", description: grantDescription },
    claim("success", grantDescription),
  ]);
  assert.deepEqual(granted.failures, []);
  assert.deepEqual(granted.info, ["Scheduler claim granted: " + claimContext]);
});
