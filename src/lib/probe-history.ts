import type { LatencyProbeResult } from "./types";

// Require both repeated failures and elapsed time. Missing reports never count as failures.
export function classifyProbeSamples(items: LatencyProbeResult[], gapSeconds: number) {
  let count = 0, since = 0, previous = NaN;
  return items.map(sample => {
    const time = Date.parse(sample.checked_at);
    if (sample.success) { count = 0; since = 0; previous = time; return "available" as const; }
    if (!Number.isFinite(time) || !Number.isFinite(previous) || time <= previous || time - previous > gapSeconds * 1000) {
      count = 0; since = time;
    }
    if (count === 0) since = time;
    count++;
    previous = time;
    return count >= 4 && time - since >= 60000 ? "unavailable" as const : "pending" as const;
  });
}

export function analyzeProbeHistory(items: LatencyProbeResult[], now: number, hours: number, gapSeconds: number) {
  const from = now - hours * 3600000;
  const samples = items.filter(item => {
    const time = Date.parse(item.checked_at);
    return time >= from && time <= now && Number.isFinite(item.delay_ms);
  }).sort((a, b) => Date.parse(a.checked_at) - Date.parse(b.checked_at) || a.id - b.id);
  const segments: LatencyProbeResult[][] = [];
  let segment: LatencyProbeResult[] = [];
  let previous: LatencyProbeResult | undefined;
  const changes: number[] = [];
  const states = classifyProbeSamples(samples, gapSeconds);
  for (const [index, sample] of samples.entries()) {
    const continuous = previous?.success && Date.parse(sample.checked_at) - Date.parse(previous.checked_at) <= gapSeconds * 1000;
    if (states[index] === "unavailable" || (sample.success && !continuous)) {
      if (segment.length) segments.push(segment);
      segment = [];
    }
    if (sample.success) {
      segment.push(sample);
      if (continuous && previous) changes.push(Math.abs(sample.delay_ms - previous.delay_ms));
    }
    if (sample.success || states[index] === "unavailable") previous = sample;
  }
  if (segment.length) segments.push(segment);
  const values = samples.filter(item => item.success).map(item => item.delay_ms).sort((a, b) => a - b);
  const failed = samples.filter(item => !item.success);
  return {
    from, samples, segments, failed, states,
    unavailable: samples.filter((_, index) => states[index] === "unavailable"),
    currentState: states.at(-1),
    latest: samples.at(-1),
    average: values.length ? values.reduce((sum, value) => sum + value, 0) / values.length : null,
    minimum: values[0] ?? null,
    maximum: values.at(-1) ?? null,
    p95: values.length ? values[Math.ceil(values.length * .95) - 1] : null,
    jitter: changes.length ? changes.reduce((sum, value) => sum + value, 0) / changes.length : null,
    loss: samples.length ? failed.length / samples.length * 100 : null,
  };
}


// Calendar-minute buckets keep missing minutes visible and never average failed samples as zero.
export function aggregateProbeMinutes(items: LatencyProbeResult[], now: number, gapSeconds: number) {
  const end = Math.floor(now / 60000) * 60000;
  const samples = items.filter(sample => Number.isFinite(Date.parse(sample.checked_at)) && Date.parse(sample.checked_at) <= now)
    .sort((a, b) => Date.parse(a.checked_at) - Date.parse(b.checked_at) || a.id - b.id);
  const states = classifyProbeSamples(samples, gapSeconds);
  const buckets = Array.from({ length: 16 }, (_, index) => ({
    start: end - (15 - index) * 60000, delay_ms: null as number | null,
    successCount: 0, totalCount: 0, unavailable: false,
  }));
  for (const [index, sample] of samples.entries()) {
    const minute = Math.floor(Date.parse(sample.checked_at) / 60000) * 60000;
    const bucket = buckets[(minute - buckets[0].start) / 60000];
    if (!bucket) continue;
    bucket.totalCount++;
    bucket.unavailable = states[index] === "unavailable";
    if (sample.success && Number.isFinite(sample.delay_ms) && sample.delay_ms >= 0) {
      bucket.delay_ms = ((bucket.delay_ms ?? 0) * bucket.successCount + sample.delay_ms) / (bucket.successCount + 1);
      bucket.successCount++;
    }
  }
  return buckets;
}
