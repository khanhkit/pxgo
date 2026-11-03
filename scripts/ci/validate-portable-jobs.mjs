import { readFile } from "node:fs/promises";

export const ALLOWED_RUNNERS = new Set([
  "ubuntu-24.04-arm",
  "ubuntu-24.04",
  "ubuntu-latest",
  "windows-latest",
  "windows-11-arm",
  "macos-15-intel",
  "macos-26-intel",
]);
const CLASSES = new Set([
  "portable-compute",
  "platform-validation",
  "canonical-release",
  "fixed-compute",
]);
const MUTATIONS = new Set(["readonly", "artifact", "mutation", "release", "claim-guarded-review"]);

export function validatePortableJobs(manifest) {
  if (!manifest || manifest.schema !== 1 || !manifest.jobs || typeof manifest.jobs !== "object")
    throw new Error("portable jobs manifest schema=1 with jobs is required");
  for (const [id, job] of Object.entries(manifest.jobs)) {
    if (!CLASSES.has(job.class)) throw new Error(`${id}: unknown class ${job.class}`);
    if (!MUTATIONS.has(job.mutationClass))
      throw new Error(`${id}: unknown mutationClass ${job.mutationClass}`);
    if (!Array.isArray(job.compatibleRunners) || job.compatibleRunners.length === 0)
      throw new Error(`${id}: compatibleRunners required`);
    if ("waitOnExhaustion" in job && typeof job.waitOnExhaustion !== "boolean")
      throw new Error(`${id}: waitOnExhaustion must be boolean`);
    if (job.waitOnExhaustion === true && !job.fallbackEnabled)
      throw new Error(`${id}: waitOnExhaustion requires fallbackEnabled=true`);
    const seen = new Set();
    for (const runner of job.compatibleRunners) {
      if (!ALLOWED_RUNNERS.has(runner)) throw new Error(`${id}: unknown runner ${runner}`);
      if (seen.has(runner)) throw new Error(`${id}: duplicate runner ${runner}`);
      seen.add(runner);
    }
    const c = job.compatibility;
    if (
      !c ||
      !Array.isArray(c.os) ||
      !c.os.length ||
      !Array.isArray(c.arch) ||
      !c.arch.length ||
      !Array.isArray(c.runtimeRequirements) ||
      !c.runtimeRequirements.length
    )
      throw new Error(`${id}: compatibility dimensions are required`);
    if (job.fallbackEnabled) {
      if (job.class !== "portable-compute")
        throw new Error(`${id}: fallback requires portable-compute class`);
      const guardedReview = job.mutationClass === "claim-guarded-review";
      if (job.mutationClass !== "readonly" && !guardedReview) {
        throw new Error(`${id}: fallback requires readonly mutationClass or claim-guarded-review`);
      }
      if (guardedReview && job.claimBeforeMutation !== true) {
        throw new Error(`${id}: claim-guarded-review fallback requires claimBeforeMutation=true`);
      }
      if (job.compatibleRunners.length < 2)
        throw new Error(`${id}: fallback requires at least two compatible runners`);
      if (
        !Number.isInteger(job.queueTimeoutSeconds) ||
        job.queueTimeoutSeconds < 30 ||
        job.queueTimeoutSeconds > 60
      )
        throw new Error(`${id}: queue timeout must be 30..60 seconds`);
    }
  }
  return manifest;
}

export async function loadPortableJobs(path) {
  return validatePortableJobs(JSON.parse(await readFile(path, "utf8")));
}

if (process.argv[1] && import.meta.url === new URL(`file://${process.argv[1]}`).href) {
  const file = process.argv[2] ?? ".github/ci/portable-jobs.json";
  await loadPortableJobs(file);
  console.log(`portable job manifest valid: ${file}`);
}
