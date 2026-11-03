import assert from "node:assert/strict";
import test from "node:test";
import { isRetryableGhRead, parseWorkerInputs } from "../../../scripts/ci/github-runner-fallback.mjs";
import { summarizeRunnerSamples } from "../../../scripts/ci/summarize-runner-fallback.mjs";
import { waitForAttemptCompletion } from "../../../scripts/ci/runner-fallback.mjs";

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
