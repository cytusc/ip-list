import { test } from 'node:test';
import assert from 'node:assert/strict';
import { analyzeProbeHistory, classifyProbeSamples, p95TrimmedProbeAverage } from '../../src/lib/probe-history.ts';
const now = Date.parse('2026-10-26T04:00:00Z');
const sample = (id, secondsAgo, delay, success = true) => ({id, ip:'1.1.1.1', delay_ms:delay, success, checked_at:new Date(now-secondsAgo*1000).toISOString()});
test('isolated timeouts are skipped while reporting gaps split lines', () => {
 const data = analyzeProbeHistory([sample(1,600,10),sample(2,540,20),sample(3,480,0,false),sample(4,420,30),sample(5,60,40)],now,1,180);
 assert.deepEqual(data.segments.map(s=>s.map(p=>p.id)),[[1,2,4],[5]]);
 assert.equal(data.jitter,10);
 assert.equal(data.average,25);
 assert.equal(data.p95,40);
 assert.equal(data.loss,20);
});
test('real time window preserves trailing silence and excludes future or old samples',()=> {
 const data=analyzeProbeHistory([sample(1,7200,1),sample(2,900,50),sample(3,-5,1)],now,1,180);
 assert.deepEqual(data.samples.map(p=>p.id),[2]);
 assert.equal(data.from,now-3600000);
 assert.equal(data.loss,0);
 assert.equal(data.jitter,null);
 assert.equal(data.latest.id,2);
});
test('all timeouts have no fabricated latency and empty samples have no loss rate',()=> {
 const data=analyzeProbeHistory([sample(1,60,0,false)],now,1,180);
 assert.equal(data.average,null);assert.equal(data.minimum,null);assert.equal(data.p95,null);
 assert.equal(data.segments.length,0);assert.equal(data.loss,100);
 assert.equal(analyzeProbeHistory([],now,1,180).loss,null);
});

test('regional quality targets distinguish nearby destinations and US regions', async () => {
 const { latencyQuality, latencyThresholds } = await import('../../src/lib/latency-quality.ts');
 for (const [country, region, city, green, yellow] of [
  ['HK','','',50,80], ['MO','','',50,80], ['TW','','',60,100],
  ['JP','','',70,110], ['KR','','',70,110], ['SG','','',80,120], ['MY','','',100,150],
  ['US','California','Los Angeles',160,200], ['US','CA','',160,200],
  ['US','加利福尼亚州','洛杉矶',160,200], ['US','','San Jose',160,200],
  ['US','DC','Washington',200,240], ['US','Washington','Seattle',160,200], ['US','New York','',200,240], ['US','NJ','',200,240],
  ['US','Texas','Dallas',180,220], ['US','','',160,200],
  [undefined,'','',200,250],
 ]) {
  const thresholds = latencyThresholds(country,region,city);
  assert.equal(thresholds.green,green); assert.equal(thresholds.yellow,yellow);
  assert.equal(latencyQuality(green,true,country,region,city),'green');
  assert.equal(latencyQuality(green+0.01,true,country,region,city),'yellow');
  assert.equal(latencyQuality(yellow,true,country,region,city),'yellow');
  assert.equal(latencyQuality(yellow+0.01,true,country,region,city),'red');
 }
 assert.equal(latencyQuality(0,false,'HK'),'red');
 assert.equal(latencyQuality(null,null,'HK'),'unknown');
 assert.equal(latencyQuality(NaN,true,'US'),'unknown');
 assert.equal(latencyQuality(180,true,'US','California'),'yellow');
 assert.equal(latencyQuality(180,true,'US','New York'),'green');
});


test('isolated failures stay pending; four consecutive failures over 60 seconds confirm outage', () => {
 const items = [sample(1,120,80),sample(2,100,0,false),sample(3,80,0,false),sample(4,60,0,false),sample(5,40,0,false),sample(6,20,85)];
 assert.deepEqual(classifyProbeSamples(items,106),['available','pending','pending','pending','unavailable','available']);
 const data=analyzeProbeHistory(items,now,1,106);
 assert.deepEqual(data.unavailable.map(s=>s.id),[5]);
 assert.equal(data.currentState,'available');
 assert.equal(data.average,82.5);
 assert.deepEqual(data.segments.map(s=>s.map(p=>p.id)),[[1],[6]]);
});
test('success resets failure streak and silence or duplicate timestamps cannot confirm an outage', () => {
 assert.deepEqual(classifyProbeSamples([sample(1,120,0,false),sample(2,100,10),sample(3,80,0,false),sample(4,60,0,false)],106),['pending','available','pending','pending']);
 assert.deepEqual(classifyProbeSamples([sample(1,600,0,false),sample(2,40,0,false),sample(3,20,0,false),sample(4,0,0,false)],106),['pending','pending','pending','pending']);
 assert.deepEqual(classifyProbeSamples([1,2,3,4].map(i=>sample(i,0,0,false)),106),['pending','pending','pending','pending']);
 assert.deepEqual(classifyProbeSamples([sample(1,3,0,false),sample(2,2,0,false),sample(3,1,0,false),sample(4,0,0,false)],106),['pending','pending','pending','pending']);
});

test('minute cells average successful samples and ignore an isolated timeout', async () => {
 const { aggregateProbeMinutes } = await import('../../src/lib/probe-history.ts');
 const end=Date.parse('2026-10-26T04:01:00Z');
 const s=(id,seconds,delay,success=true)=>({id,ip:'1.1.1.1',checked_at:new Date(end-seconds*1000).toISOString(),delay_ms:delay,success});
 const cells=aggregateProbeMinutes([s(1,60,70),s(2,40,0,false),s(3,20,90),s(4,0,110)],end,106);
 assert.equal(cells.length,16);
 assert.equal(cells[14].delay_ms,80);
 assert.equal(cells[14].successCount,2);assert.equal(cells[14].totalCount,3);
 assert.equal(cells[14].unavailable,false);
 assert.equal(cells[15].delay_ms,110);
 assert.equal(cells[13].delay_ms,null);
 assert.equal(cells[15].start-cells[14].start,60000);
});
test('minute cells retain missing minutes and confirm sustained loss across minute boundaries', async () => {
 const { aggregateProbeMinutes } = await import('../../src/lib/probe-history.ts');
 const end=Date.parse('2026-10-26T04:01:00Z');
 const s=(id,seconds,success=false)=>({id,ip:'1.1.1.1',checked_at:new Date(end-seconds*1000).toISOString(),delay_ms:success?80:0,success});
 const cells=aggregateProbeMinutes([s(1,60),s(2,40),s(3,20),s(4,0)],end,106);
 assert.equal(cells[14].delay_ms,null);assert.equal(cells[14].unavailable,false);
 assert.equal(cells[15].delay_ms,null);assert.equal(cells[15].unavailable,true);
 const recovered=aggregateProbeMinutes([s(1,80),s(2,60),s(3,40),s(4,20),s(5,0,true)],end,106);
 assert.equal(recovered[15].delay_ms,80);assert.equal(recovered[15].unavailable,false);
 const stale=aggregateProbeMinutes([s(1,180,true)],end,106);
 assert.equal(stale[12].delay_ms,80);assert.equal(stale[15].totalCount,0);
 assert.ok(aggregateProbeMinutes([],end,106).every(c=>c.delay_ms===null && !c.unavailable));
});

test('rollups preserve weighted statistics and never fabricate percentiles or outage streaks', () => {
 const bucket = (id, secondsAgo, total, ok, sum, min, max) => ({
  ...sample(id, secondsAgo, ok ? sum / ok : 0, ok > 0), bucket_seconds:300,
  sample_count:total, success_count:ok, delay_sum:sum, min_delay_ms:min, max_delay_ms:max,
 });
 const items=[bucket(-1,12000,15,12,120,5,20),bucket(-2,11700,3,2,200,80,120),sample(1,20,40)];
 const data=analyzeProbeHistory(items,now,6,180);
 assert.equal(data.sampleCount,19);
 assert.equal(data.successCount,15);
 assert.equal(data.average,24);
 assert.equal(data.minimum,5);
 assert.equal(data.maximum,120);
 assert.equal(data.loss,4/19*100);
 assert.equal(data.p95,null);
 assert.equal(data.jitter,null);
 assert.equal(data.hasRollups,true);
 assert.deepEqual(data.failed.map(p=>p.id),[-1,-2]);
 assert.equal(data.unavailable.length,0);
 assert.deepEqual(data.segments.map(s=>s.map(p=>p.id)),[[-1,-2],[1]]);
 const failed=analyzeProbeHistory([0,1,2,3].map(i=>bucket(-i-1,12000-i*300,15,0,0,null,null)),now,6,180);
 assert.equal(failed.average,null);
 assert.equal(failed.minimum,null);
 assert.equal(failed.maximum,null);
 assert.equal(failed.loss,100);
 assert.equal(failed.unavailable.length,0);
 assert.equal(failed.segments.length,0);
});

test('summaries reset raw failure evidence and missing buckets break the curve',()=> {
 const bucket={...sample(-1,400,50),bucket_seconds:300,sample_count:15,success_count:15,delay_sum:750,min_delay_ms:40,max_delay_ms:60};
 const items=[sample(-2,1000,60),bucket,sample(1,80,0,false),sample(2,60,0,false),sample(3,40,0,false)];
 const data=analyzeProbeHistory(items,now,1,180);
 assert.deepEqual(data.segments.map(s=>s.map(p=>p.id)),[[-2],[-1]]);
 assert.equal(data.unavailable.length,0);
 assert.equal(data.currentState,'pending');
});

test('P95 trimmed average pools successful samples within the visible sixteen minutes', () => {
 const probe = samples => ({ window_end: new Date(now).toISOString(), samples });
 const low = Array.from({length:19}, (_,id) => sample(id,30,10));
 assert.equal(p95TrimmedProbeAverage([probe(low.slice(0,10)),probe([...low.slice(10),sample(20,30,1000)])]),10);
 assert.equal(p95TrimmedProbeAverage([probe([sample(1,30,10),sample(2,30,20)])]),15);
 assert.equal(p95TrimmedProbeAverage([probe([
  sample(1,30,20),sample(2,30,0,false),sample(3,30,NaN),sample(4,30,-1),sample(5,961,500),sample(6,-1,500),
 ])]),20);
 assert.equal(p95TrimmedProbeAverage([probe([sample(1,30,0,false)])]),null);
 assert.equal(p95TrimmedProbeAverage([]),null);
});
