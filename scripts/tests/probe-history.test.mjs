import { test } from 'node:test';
import assert from 'node:assert/strict';
import { analyzeProbeHistory } from '../../src/lib/probe-history.ts';
const now = Date.parse('2026-10-26T04:00:00Z');
const sample = (id, secondsAgo, delay, success = true) => ({id, ip:'1.1.1.1', delay_ms:delay, success, checked_at:new Date(now-secondsAgo*1000).toISOString()});
test('timeouts and reporting gaps split lines and jitter', () => {
 const data = analyzeProbeHistory([sample(1,600,10),sample(2,540,20),sample(3,480,0,false),sample(4,420,30),sample(5,60,40)],now,1,180);
 assert.deepEqual(data.segments.map(s=>s.map(p=>p.id)),[[1,2],[4],[5]]);
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
