#!/usr/bin/env bash
set -euo pipefail

repo="${PXGO_GH_REPO:-khanhkit/pxgo}"
ref="${1:-verify/ap0002-ap0024}"
workflow="${PXGO_AD_WORKFLOW:-real-ad-verification.yml}"
environment="${PXGO_AD_ENVIRONMENT:-pxgo-ad}"
proxy_var="${PXGO_AD_PROXY_VAR:-PXGO_SSPI_AD_PROXY_HOST}"

command -v gh >/dev/null 2>&1 || {
  echo "gh CLI is required" >&2
  exit 2
}
command -v jq >/dev/null 2>&1 || {
  echo "jq is required" >&2
  exit 2
}

gh auth status >/dev/null

proxy_host="$(
  gh api "repos/${repo}/environments/${environment}/variables/${proxy_var}" \
    --jq '.value'
)"
if [[ -z "$proxy_host" ]]; then
  echo "environment variable ${proxy_var} is empty" >&2
  exit 3
fi

runner_json="$(
  gh api "repos/${repo}/actions/runners" --jq \
    '[.runners[]
      | select(.status == "online")
      | select([.labels[].name] | index("self-hosted"))
      | select([.labels[].name] | index("windows"))
      | select([.labels[].name] | index("x64"))
      | select([.labels[].name] | index("pxgo-ad"))][0] // empty'
)"
if [[ -z "$runner_json" ]]; then
  echo "no online self-hosted/windows/x64/pxgo-ad runner is registered for ${repo}" >&2
  exit 4
fi

runner_name="$(jq -r '.name' <<<"$runner_json")"
echo "PASS: protected AD runner online: ${runner_name}"
echo "PASS: ${proxy_var}=${proxy_host}"

before_epoch="$(date +%s)"
gh workflow run "$workflow" \
  --repo "$repo" \
  --ref "$ref" \
  -f "verification_ref=${ref}"

run_id=""
for _ in $(seq 1 30); do
  run_id="$(
    gh run list \
      --repo "$repo" \
      --workflow "$workflow" \
      --branch "$ref" \
      --event workflow_dispatch \
      --limit 10 \
      --json databaseId,createdAt \
      --jq ".[] | select((.createdAt | fromdateiso8601) >= ${before_epoch}) | .databaseId" \
      | head -n1
  )"
  [[ -n "$run_id" ]] && break
  sleep 2
done
if [[ -z "$run_id" ]]; then
  echo "workflow dispatch succeeded but run id could not be resolved" >&2
  exit 5
fi

echo "CI run: ${run_id}"

# The pxgo-ad environment is intentionally protected. The authorized caller may
# approve its own deployment because prevent_self_review is disabled for this
# single-user verification fork. On an authoritative multi-user upstream repo,
# keep prevent_self_review enabled and let a distinct reviewer approve.
for _ in $(seq 1 90); do
  pending="$(
    gh api "repos/${repo}/actions/runs/${run_id}/pending_deployments" 2>/dev/null || echo '[]'
  )"
  env_id="$(
    jq -r --arg env "$environment" \
      '.[] | select(.environment.name == $env) | .environment.id' \
      <<<"$pending" \
      | head -n1
  )"
  if [[ -n "$env_id" && "$env_id" != "null" ]]; then
    payload="$(
      jq -n \
        --argjson env_id "$env_id" \
        --arg comment "Autopilot approval for protected PxGo real-AD verification" \
        '{environment_ids:[$env_id],state:"approved",comment:$comment}'
    )"
    gh api \
      --method POST \
      "repos/${repo}/actions/runs/${run_id}/pending_deployments" \
      --input - <<<"$payload" >/dev/null
    echo "PASS: approved protected ${environment} deployment"
    break
  fi

  status="$(gh run view "$run_id" --repo "$repo" --json status --jq '.status')"
  [[ "$status" == "completed" ]] && break
  sleep 2
done

gh run watch "$run_id" --repo "$repo" --exit-status

echo "PASS: exact-ref protected real-AD verification succeeded"
