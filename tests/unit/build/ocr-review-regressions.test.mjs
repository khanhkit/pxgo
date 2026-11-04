import assert from "node:assert/strict";
import { access, mkdir, mkdtemp, readFile, rename, rm, stat, symlink, writeFile } from "node:fs/promises";
import vm from "node:vm";
import { spawnSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join } from "node:path";
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
  function extractClaimScript(yaml) {
    const beginMarker = "// OCR_CLAIM_GATE_BEGIN";
    const endMarker = "// OCR_CLAIM_GATE_END";
    const begin = yaml.indexOf(beginMarker);
    const end = yaml.indexOf(endMarker, begin + beginMarker.length);
    assert.ok(begin > 0 && end > begin, "trusted-base claim gate markers missing");
    const before = yaml.slice(0, begin).split("\n");
    const indentation = before.at(-1);
    // Both markers must remain in the actual script block. Infer the YAML
    // indentation rather than assuming a particular workflow formatter.
    assert.match(before.at(-2), /^\s*script: \|\s*$/);
    assert.match(indentation, /^ +$/);
    const lines = yaml.slice(begin + beginMarker.length, end).split("\n");
    return lines.map((line) => {
      if (!line.trim()) return "";
      assert.ok(line.startsWith(indentation), "claim gate escaped its script block");
      return line.slice(indentation.length);
    }).join("\n");
  }
  const script = extractClaimScript(workflow);
  // Reformatting YAML indentation must not change the executed gate.
  const reindented = workflow.replace(/^ {12}(?=\S| +\S)/gm, "              ");
  assert.equal(extractClaimScript(reindented), script);

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

// Execute the real workflow shell, not a reimplementation. Persistent runner
// temporary-file cleanup must never unlink a still-running OCR CLI.
function ocrWorkerShellStep(workflow, name) {
  const marker = "      - name: " + name;
  const start = workflow.indexOf(marker);
  assert.ok(start >= 0, "worker step missing: " + name);
  const header = "        run: |" + String.fromCharCode(10);
  const scriptStart = workflow.indexOf(header, start);
  assert.ok(scriptStart >= 0, "no shell body for " + name);
  const begin = scriptStart + header.length;
  const end = workflow.indexOf(String.fromCharCode(10) + "      - name:", begin);
  return workflow.slice(begin, end < 0 ? workflow.length : end)
    .split(String.fromCharCode(10))
    .map((line) => line.startsWith("          ") ? line.slice(10) : line)
    .join(String.fromCharCode(10));
}

test("actual OCR npm prefix and cleanup survive shared temp erasure and symlink attacks", async () => {
  const workflow = await readFile(
    new URL("../../../.github/workflows/ocr-review-worker.yml", import.meta.url), "utf8"
  );
  const setup = ocrWorkerShellStep(workflow, "Isolate OCR CLI npm prefix for this worker run");
  const cleanup = ocrWorkerShellStep(workflow, "Clean isolated OCR CLI npm prefix");
  const root = await mkdtemp(join(tmpdir(), "pxgo-ocr-worker-prefix-"));
  try {
    const home = join(root, "home");
    const temp = join(root, "runner-temp");
    const outsider = join(root, "unrelated");
    const ghEnv = join(root, "github-env");
    const ghPath = join(root, "github-path");
    await mkdir(home);
    await mkdir(temp);
    await mkdir(outsider);
    await writeFile(join(outsider, "keep"), "protected");
    await writeFile(ghEnv, "");
    await writeFile(ghPath, "");
    await symlink(outsider, join(home, ".cache"), "dir");

    const run = (script, attempt, runId = "741") => {
      const env = {
        ...process.env,
        HOME: home,
        RUNNER_TEMP: temp,
        GITHUB_RUN_ID: runId,
        GITHUB_RUN_ATTEMPT: String(attempt),
        GITHUB_ENV: ghEnv,
        GITHUB_PATH: ghPath,
      };
      return spawnSync("bash", ["-e", "-o", "pipefail", "-c", script], {
        env, encoding: "utf8",
      });
    };
    const prefix = (attempt) => join(home, ".pxgo-ocr-npm-741-" + attempt);
    for (const attempt of [1, 2]) {
      const result = run(setup, attempt);
      assert.equal(result.status, 0, result.stderr);
      const mode = (await stat(prefix(attempt))).mode & 0o077;
      assert.equal(mode, 0, "npm prefix must not be accessible to other users");
      await writeFile(join(prefix(attempt), "bin", "ocr"), "binary-" + attempt);
    }

    await rm(temp, { recursive: true });
    assert.equal(await readFile(join(prefix(1), "bin", "ocr"), "utf8"), "binary-1");
    assert.equal(await readFile(join(prefix(2), "bin", "ocr"), "utf8"), "binary-2");
    assert.equal(await readFile(join(outsider, "keep"), "utf8"), "protected");
    assert.match(await readFile(ghEnv, "utf8"), /NPM_CONFIG_PREFIX=/);

    for (const malformed of ["", "0", "abc", "0001", "1-2", "../"]) {
      assert.notEqual(run(setup, 3, malformed).status, 0, "accepted run ID " + malformed);
    }
    await symlink(outsider, prefix(3), "dir");
    assert.notEqual(run(setup, 3).status, 0, "preexisting symlink accepted as npm install root");
    assert.equal(run(cleanup, 3).status, 0);
    assert.equal(await readFile(join(outsider, "keep"), "utf8"), "protected");

    // Replace the prior install directory with a symlink before cleanup:
    // find -P must unlink the symlink and never traverse the other directory.
    const saved = join(home, "renamed-1");
    await rename(prefix(1), saved);
    await symlink(outsider, prefix(1), "dir");
    assert.equal(run(cleanup, 1).status, 0);
    await assert.rejects(access(prefix(1)));
    assert.equal(await readFile(join(saved, "bin", "ocr"), "utf8"), "binary-1");
    assert.equal(await readFile(join(outsider, "keep"), "utf8"), "protected");
    assert.equal(await readFile(join(prefix(2), "bin", "ocr"), "utf8"), "binary-2");
    assert.equal(run(cleanup, 2).status, 0);
    await assert.rejects(access(prefix(2)));
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
