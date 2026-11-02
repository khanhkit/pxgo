function median(values){
  const a=[...values].sort((x,y)=>x-y); if(!a.length) return null;
  const m=Math.floor(a.length/2); return a.length%2?a[m]:(a[m-1]+a[m])/2;
}
export function buildFallbackSummary(result){
  const lines=['## Multi-runner fallback','','| Attempt | Runner | Queue (s) | Outcome |','|---:|---|---:|---|'];
  for(const a of result.attempts??[]) lines.push(`| ${a.attempt} | ${a.runner} | ${a.queueSeconds??''} | ${a.outcome??''} |`);
  lines.push('',`Final execution runner: ${result.runner?`\`${result.runner}\``:'none'}`,'');
  return lines.join('\n');
}
export function summarizeRunnerSamples(samples){
  const groups=new Map();
  for(const s of samples){
    if(!groups.has(s.runner)) groups.set(s.runner,[]); groups.get(s.runner).push(s);
  }
  return [...groups].map(([runner,rows])=>({
    runner,
    samples:rows.length,
    medianQueueSeconds:median(rows.map(r=>r.startedAt-r.queuedAt)),
    medianExecutionSeconds:median(rows.map(r=>r.completedAt-r.startedAt)),
    medianTotalSeconds:median(rows.map(r=>r.completedAt-r.queuedAt)),
    successRate:rows.filter(r=>r.conclusion==='success').length/rows.length,
  })).sort((a,b)=>a.medianTotalSeconds-b.medianTotalSeconds || a.runner.localeCompare(b.runner));
}
