import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");
const read = (p) => readFile(path.join(root, p), "utf8");

test("trusted-base OCR dispatcher only handles same-repo non-draft PRs to main", async () => {
  const y = await read(".github/workflows/ocr-review.yml");
  assert.match(y, /pull_request:/);
  assert.doesNotMatch(y, /pull_request_target:/);
  assert.match(y, /branches:\s*\[main\]/);
  for (const type of ["opened", "synchronize", "reopened", "ready_for_review"]) {
    assert.ok(y.includes(type), type);
  }
  assert.match(y, /draft\s*==\s*false/);
  assert.match(y, /head\.repo\.full_name\s*==\s*github\.repository/);
  assert.match(y, /cancel-in-progress:\s*true/);
  assert.match(y, /runs-on:\s*ubuntu-24\.04-arm/);
  assert.ok(y.includes("github.event.pull_request.base.sha"));
  assert.ok(y.includes("persist-credentials: false"));
  assert.match(y, /node-version:\s*'24'/);
  for (const dependency of [
    "scripts/ci/github-runner-fallback.mjs",
    ".github/ci/ocr-review-job.json",
    ".github/workflows/ocr-review-worker.yml",
  ]) assert.ok(y.includes(dependency), dependency);
  assert.ok(y.includes("--wait-completion"));
  assert.ok(y.includes("--worker-input \"pr_number="));
  assert.ok(y.includes("--worker-input \"base_ref="));
  assert.ok(y.includes("--worker-input \"base_sha="));
  assert.ok(y.includes("if grep -Fq '      base_sha:' .github/workflows/ocr-review-worker.yml"));
  assert.doesNotMatch(y, /OCR_LLM_AUTH_TOKEN|alibaba\/open-code-review@/);
});

test("worker validates PR SHA and repo before using OCR secrets", async () => {
  const y = await read(".github/workflows/ocr-review-worker.yml");
  assert.match(y, /workflow_dispatch:/);
  assert.doesNotMatch(y, /pull_request_target:|pull_request:|\n\s+push:/);
  for (const permission of ["contents: read", "statuses: write", "pull-requests: write"]) {
    assert.ok(y.includes(permission), permission);
  }
  assert.ok(y.includes("runs-on: " + "$" + "{{ inputs.runner }}"));
  assert.match(y, /cancel-in-progress:\s*true/);
  assert.doesNotMatch(y, /actions\/checkout@|npm\s+(?:ci|install)|uses:\s*\.\//);
  for (const check of [
    "github.rest.pulls.get",
    "pullRequest.state !== 'open'",
    "pullRequest.draft",
    "pullRequest.head.repo?.full_name !== process.env.GITHUB_REPOSITORY",
    "pullRequest.head.sha !== process.env.SOURCE_SHA",
    "pullRequest.base.ref !== process.env.BASE_REF",
    "pullRequest.base.sha !== process.env.BASE_SHA",
    "context.sha !== process.env.BASE_SHA",
    "Wait for scheduler claim before secret-bearing review",
    "Revalidate requested PR context after scheduler claim",
    "RUNNER_READY_CONTEXT",
    "CLAIM_CONTEXT",
    "EXPECTED_RUNNER_LABEL",
    "claim?.state === 'success' && claim.description === expectedGrant",
    "claim?.state === 'success' || claim?.state === 'failure'",
  ]) assert.ok(y.includes(check), check);
  assert.ok(y.includes("const expectedGrant = 'runner claim granted: ' + process.env.EXPECTED_RUNNER_LABEL"));
  assert.doesNotMatch(y, /if \(claim\?\.state === 'success'\) \{/);
  const a = y.indexOf("Wait for scheduler claim before secret-bearing review");
  const b = y.indexOf("Revalidate requested PR context after scheduler claim");
  const c = y.indexOf("Detect OCR configuration");
  const isolate = y.indexOf("Isolate OCR CLI npm prefix for this worker run");
  const d = y.indexOf("Run OpenCodeReview");
  assert.ok(a >= 0 && b > a && c > b && isolate > c && d > isolate);
  assert.ok(y.includes('NPM_CONFIG_PREFIX=${prefix}'));
  assert.ok(y.includes('echo "${prefix}/bin" >> "$GITHUB_PATH"'));
  assert.match(y, /prefix="\$\{RUNNER_TEMP\}\/pxgo-ocr-npm-\$\{GITHUB_RUN_ID\}-\$\{GITHUB_RUN_ATTEMPT\}"/);
  for (const input of ["OCR_LLM_URL", "OCR_LLM_AUTH_TOKEN", "OCR_LLM_MODEL", "OCR_LLM_USE_ANTHROPIC"]) {
    assert.ok(y.includes(input), input);
  }
  assert.ok(y.includes("steps.ocr-config.outputs.ready == 'true'"));
  assert.ok(y.includes("alibaba/open-code-review@375f64cd6f67a48a135da83cfcf59c52669a09cb"));
  for (const key of ["ocr_version: \"1.12.13\"", "sticky_summary: \"true\"",
    "incremental: \"true\"", "checkpoint_range: \"true\"",
    "stream_progress: \"true\"", "upload_artifacts: \"true\"",
    "max_tokens_budget: \"0\"", "rule: .opencodereview/rule.json"]) {
    assert.ok(y.includes(key), key);
  }
});

test("fallback runner configuration is claim guarded and contains no Windows runner", async () => {
  const m = JSON.parse(await read(".github/ci/ocr-review-job.json"));
  assert.equal(m.schema, 1);
  assert.equal(m.defaultQueueTimeoutSeconds, 45);
  const job = m.jobs["ocr-review"];
  assert.equal(job.class, "portable-compute");
  assert.equal(job.mutationClass, "claim-guarded-review");
  assert.equal(job.claimBeforeMutation, true);
  assert.equal(job.fallbackEnabled, true);
  assert.equal(job.waitOnExhaustion, true);
  assert.equal(job.queueTimeoutSeconds, 45);
  assert.deepEqual(job.compatibleRunners, ["ubuntu-24.04-arm", "ubuntu-latest", "macos-15-intel"]);
});

test("PxGo fallback scheduler has isolated claim and readiness namespaces", async () => {
  const scheduler = await read("scripts/ci/runner-fallback.mjs");
  const adapter = await read("scripts/ci/github-runner-fallback.mjs");
  assert.ok(scheduler.includes("pxgo/runner-claim/"));
  assert.ok(adapter.includes("pxgo/runner-claim/"));
  assert.ok(adapter.includes("pxgo/runner-ready/"));
  assert.doesNotMatch(scheduler + adapter, /agentproxy\/runner-/);
  assert.ok(scheduler.includes('await adapter.cancelAttempt(attempt)'));
  assert.ok(scheduler.includes('await adapter.setClaim(attempt, "granted")'));
});

test("PxGo OCR project rule merges default language rules and covers proxy security", async () => {
  const doc = JSON.parse(await read(".opencodereview/rule.json"));
  assert.equal(doc.rules.length, 1);
  assert.equal(doc.rules[0].path, "**/*");
  assert.equal(doc.rules[0].merge_system_rule, true);
  const rule = doc.rules[0].rule;
  assert.match(rule, /PxGo.*authoritative/i);
  for (const focus of ["NTLM", "Kerberos", "PAC", "DNS", "DoH", "SSRF", "data races"]) {
    assert.match(rule, new RegExp(focus, "i"));
  }
});

test("persistent Windows real-AD runner removes stale extracted files", async () => {
  const y = await read(".github/workflows/real-ad-verification.yml");
  assert.match(y, /Remove-Item -LiteralPath source -Recurse -Force/);
});


test("main CI executes deterministic OCR fallback regression, not only static contract", async () => {
  const y = await read(".github/workflows/ci.yml");
  assert.match(y, /Verify OpenCodeReview workflow and claim-fallback regressions/);
  assert.match(y, /node --test tests\/unit\/build\/ocr-review-workflow-contract\.test\.mjs/);
  assert.match(y, /tests\/unit\/build\/ocr-review-regressions\.test\.mjs/);
});
