import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");
async function readOptional(relativePath) {
  try {
    return await readFile(path.join(repoRoot, relativePath), "utf8");
  } catch (error) {
    if (error?.code === "ENOENT") return "";
    throw error;
  }
}

test("OCR dispatcher is same-repo pull_request only and runs trusted control-plane code from base", async () => {
  const y = await readOptional(".github/workflows/ocr-review.yml");
  assert.match(y, /pull_request:/);
  assert.doesNotMatch(y, /pull_request_target:/);
  assert.match(y, /branches:\\s*\\[main\\]/);
  assert.match(
    y,
    /types:\s*\[[^\]]*opened[^\]]*synchronize[^\]]*reopened[^\]]*ready_for_review[^\]]*\]/s
  );
  assert.match(y, /draft\s*==\s*false/);
  assert.match(y, /head\.repo\.full_name\s*==\s*github\.repository/);
  assert.match(y, /cancel-in-progress:\s*true/);
  assert.match(y, /runs-on:\s*ubuntu-24\.04-arm/);
  assert.match(y, /ref:\s*\$\{\{\s*github\.event\.pull_request\.base\.sha\s*\}\}/);
  assert.match(y, /id:\s*base-contract/);
  assert.match(y, /ocr-review-worker\.yml/);
  assert.match(y, /ocr-review-job\.json/);
  assert.match(y, /github-runner-fallback\.mjs/);\n  assert.match(y, /node-version:\\s*[\x27\x22]24[\x27\x22]/);
  assert.match(y, /if:\s*steps\.base-contract\.outputs\.ready\s*==\s*['\"]true['\"]/);
  assert.match(y, /--manifest\s+\.github\/ci\/ocr-review-job\.json/);
  assert.match(y, /--worker-workflow\s+ocr-review-worker\.yml/);
  assert.match(y, /--wait-completion/);
  assert.match(y, /--worker-input\s+"?pr_number=/);
  assert.match(y, /--worker-input\s+"?base_ref=/);
  assert.doesNotMatch(y, /OCR_LLM_AUTH_TOKEN|alibaba\/open-code-review@/);
});

test("OCR worker is manual-only, claim-gated, never executes PR-head repository code, and pins review behavior", async () => {
  const y = await readOptional(".github/workflows/ocr-review-worker.yml");
  assert.match(y, /workflow_dispatch:/);
  assert.doesNotMatch(y, /pull_request(?:_target)?:|\n\s+push:/);
  assert.match(y, /statuses:\s*write/);
  assert.match(y, /contents:\s*read/);
  assert.match(y, /pull-requests:\s*write/);
  assert.match(y, /group:\s*ocr-review-\$\{\{\s*inputs\.pr_number\s*\}\}/);
  assert.match(y, /cancel-in-progress:\s*true/);
  assert.match(y, /runs-on:\s*\$\{\{\s*inputs\.runner\s*\}\}/);
  assert.doesNotMatch(y, /actions\/checkout@|npm\s+(?:ci|install)|uses:\s*\.\//);
  assert.match(y, /actions\/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3/);
  assert.match(y, /github\.rest\.pulls\.get/);
  assert.match(y, /pullRequest\.head\.repo\?\.full_name\s*!==\s*process\.env\.GITHUB_REPOSITORY/);
  assert.match(y, /pullRequest\.head\.sha\s*!==\s*process\.env\.SOURCE_SHA/);
  assert.match(y, /pullRequest\.base\.ref\s*!==\s*process\.env\.BASE_REF/);
  assert.match(y, /pullRequest\.draft/);
  assert.match(y, /claim_context/);
  assert.match(y, /ready_context/);
  assert.match(y, /statuses:\s*write/);
  assert.match(y, /createCommitStatus/);
  assert.match(y, /RUNNER_READY_CONTEXT/);
  const waitIndex = y.indexOf("Wait for scheduler claim before secret-bearing review");
  const revalidateIndex = y.indexOf("Revalidate requested PR context after scheduler claim");
  const configIndex = y.indexOf("Detect OCR configuration");
  assert.ok(waitIndex >= 0 && revalidateIndex > waitIndex && configIndex > revalidateIndex);
  const revalidate = y.slice(revalidateIndex, configIndex);
  assert.match(revalidate, /github\.rest\.pulls\.get/);
  assert.match(revalidate, /pullRequest\.state\s*!==\s*['"]open['"]/);
  assert.match(
    revalidate,
    /pullRequest\.head\.repo\?\.full_name\s*!==\s*process\.env\.GITHUB_REPOSITORY/
  );
  assert.match(revalidate, /pullRequest\.head\.sha\s*!==\s*process\.env\.SOURCE_SHA/);
  assert.match(revalidate, /pullRequest\.base\.ref\s*!==\s*process\.env\.BASE_REF/);
  assert.match(y, /id:\s*ocr-config/);
  assert.match(y, /OCR_LLM_URL:\s*\$\{\{\s*secrets\.OCR_LLM_URL\s*\}\}/);
  assert.match(y, /OCR_LLM_AUTH_TOKEN:\s*\$\{\{\s*secrets\.OCR_LLM_AUTH_TOKEN\s*\}\}/);
  assert.match(y, /OCR_LLM_MODEL:\s*\$\{\{\s*vars\.OCR_LLM_MODEL\s*\}\}/);
  assert.match(y, /OCR_LLM_USE_ANTHROPIC:\s*\$\{\{\s*vars\.OCR_LLM_USE_ANTHROPIC\s*\}\}/);
  assert.match(y, /ready=false/);
  assert.match(y, /OpenCodeReview configuration unavailable/);
  assert.match(y, /if:\s*steps\.ocr-config\.outputs\.ready\s*==\s*['\"]true['\"]/);
  assert.match(y, /alibaba\/open-code-review@fabbdb296b0d97e2140ada8d54ca7d6d6d1d7ad4/);
  assert.match(y, /ocr_version:\s*["']1\.12\.13["']/);
  assert.match(y, /pr_number:\s*\$\{\{\s*inputs\.pr_number\s*\}\}/);
  assert.match(y, /base_ref:\s*\$\{\{\s*inputs\.base_ref\s*\}\}/);
  assert.match(y, /head_sha:\s*\$\{\{\s*inputs\.source_sha\s*\}\}/);
  assert.match(y, /effort:\s*["']medium["']/);
  assert.match(y, /sticky_summary:\s*["']true["']/);
  assert.match(y, /incremental:\s*["']true["']/);
  assert.match(y, /checkpoint_range:\s*["']true["']/);
  assert.match(y, /resolve_outdated:\s*["']report["']/);
  assert.match(y, /stream_progress:\s*["']true["']/);
  assert.match(y, /upload_artifacts:\s*["']true["']/);
  assert.match(y, /max_tokens_budget:\s*["']0["']/);
  assert.match(y, /rule:\s*\.opencodereview\/rule\.json/);
  assert.doesNotMatch(y, /timeout-minutes:/);
});

test("OCR runner manifest enables one sequential claim-guarded review across proven POSIX candidates", async () => {
  const raw = await readOptional(".github/ci/ocr-review-job.json");
  assert.notEqual(raw, "");
  const manifest = JSON.parse(raw);
  assert.equal(manifest.schema, 1);
  assert.equal(manifest.defaultQueueTimeoutSeconds, 45);
  const job = manifest.jobs["ocr-review"];
  assert.equal(job.class, "portable-compute");
  assert.equal(job.mutationClass, "claim-guarded-review");
  assert.equal(job.claimBeforeMutation, true);
  assert.equal(job.fallbackEnabled, true);
  assert.equal(job.queueTimeoutSeconds, 45);
  assert.equal(job.waitOnExhaustion, true);
  assert.deepEqual(job.compatibleRunners, ["ubuntu-24.04-arm", "ubuntu-latest", "macos-15-intel"]);
  assert.equal(
    job.compatibleRunners.some((runner) => runner.startsWith("windows-")),
    false
  );
});

test("OCR rule overlay preserves PxGo project contracts and adds proxy-focused semantic review", async () => {
  const raw = await readOptional(".opencodereview/rule.json");
  assert.notEqual(raw, "");
  const ruleFile = JSON.parse(raw);
  assert.equal(ruleFile.rules.length, 1);
  assert.equal(ruleFile.rules[0].path, "**/*");
  assert.equal(ruleFile.rules[0].merge_system_rule, true);
  const rule = ruleFile.rules[0].rule;
  assert.match(rule, /AGENTS\.md.*authoritative/i);
  for (const focus of ["NTLM", "Kerberos", "PAC", "DNS", "DoH", "SSRF", "data races"]) {
    assert.match(rule, new RegExp(focus, "i"));
  }
});

test("PxGo runner claim namespace is isolated from AgentProxy", async () => {
  for (const file of ["scripts/ci/github-runner-fallback.mjs", "scripts/ci/runner-fallback.mjs"]) {
    const text = await readOptional(file);
    assert.match(text, /pxgo\\/runner-/);
    assert.doesNotMatch(text, /agentproxy\\/runner-/);
  }
});
