package main

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestDNSRefusesCompleteDeletionWithLiveControl(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	_, err := a.db.Exec(ctx, `
 INSERT INTO latency_probes(name,token_hash,last_seen,heartbeat_at) VALUES('selected','selected',now(),now());
 INSERT INTO latency_probe_results(probe_id,ip,success,delay_ms,checked_at)
 SELECT id,'1.1.1.1',true,20,now() FROM latency_probes;
 INSERT INTO nodes(name,address,port,share_uri) VALUES('incident-fixture','sample.invalid',443,'vless://test@sample.invalid:443?type=grpc&security=tls');
 INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at,config_hash,certificate)
 SELECT 'grpc',ip::inet,true,40,now(),md5('vless://test@sample.invalid:443?type=grpc&security=tls'),'{"status":"passed"}'::jsonb
 FROM unnest(ARRAY['47.243.238.144','23.129.140.165']) ip;
 INSERT INTO latency_probe_results(probe_id,ip,success,delay_ms,checked_at)
 SELECT id,ip::inet,false,0,now()-make_interval(secs=>age) FROM latency_probes
 CROSS JOIN unnest(ARRAY['47.243.238.144','23.129.140.165']) ip CROSS JOIN generate_series(1,301,20) age;
 INSERT INTO dns_managed_records(domain,zone_id,name,type) VALUES
 ('reiiv.com','dns-test-zone','hk.reiiv.com','A'),('reiiv.com','dns-test-zone','jp.reiiv.com','A');`)
	if err != nil {
		t.Fatal(err)
	}
	fake := &dnsFakeProvider{records: []dnsRemoteRecord{
		{ID: "hk", Name: "hk.reiiv.com", Type: "A", Line: "default_view", Status: "ACTIVE", TTL: 300, Values: []string{"47.243.238.144"}},
		{ID: "jp", Name: "jp.reiiv.com", Type: "A", Line: "default_view", Status: "ACTIVE", TTL: 300, Values: []string{"23.129.140.165"}},
	}}
	for _, mode := range []string{"preview", "sync"} {
		report := dnsReport{Mode: mode}
		if err := a.executeDNS(ctx, dnsTestConfig(), &report, fake); err != nil {
			t.Fatal(err)
		}
		if len(fake.writes) != 0 || len(report.Records) != 2 || !strings.Contains(report.Warning, "全部解析") {
			t.Fatalf("unsafe report: %+v writes=%+v", report, fake.writes)
		}
		if len(report.Candidates) != 2 {
			t.Fatalf("missing failed candidates: %+v", report.Candidates)
		}
		for _, item := range report.Candidates {
			if item.State != "failed" {
				t.Fatalf("unexpected candidate: %+v", item)
			}
		}
		for _, r := range report.Records {
			if r.Action != "hold" || r.Applied || !slices.Equal(r.Current, r.Desired) {
				t.Fatalf("lost record: %+v", r)
			}
		}
	}
}
