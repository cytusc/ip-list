package main

import (
	"context"
	"testing"
)

func TestConnectivityDataRetention(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	exec := func(query string) {
		t.Helper()
		if _, err := a.db.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO nodes(name,address,port,share_uri,enabled) VALUES
 ('current','current.invalid',443,'current',true),
 ('disabled','disabled.invalid',443,'disabled',false)`)
	exec(`INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at,config_hash)
 VALUES ('grpc','192.0.2.0',true,1,now(),md5('current')),
 ('grpc','192.0.2.1',false,0,now(),md5('current')),
 ('grpc','192.0.2.2',true,1,now(),md5('replaced')),
 ('grpc','192.0.2.3',true,1,now(),md5('disabled')),
 ('vless/grpc','192.0.2.0',true,1,now(),md5('current'))`)
	exec(`INSERT INTO ip_results(node_id,ip,protocol,available,delay_ms,checked_at,config_hash)
 SELECT n.id,p.ip,p.protocol,p.available,p.delay_ms,p.checked_at,p.config_hash
 FROM node_ip_pool p JOIN nodes n ON n.name=CASE WHEN p.ip='192.0.2.3' THEN 'disabled' ELSE 'current' END
 WHERE p.protocol='grpc'`)
	exec(`INSERT INTO node_scan_rounds(job_id,round,protocol,started_at,status,updated_at) VALUES
 ('expired',1,'grpc',now()-interval '8 days','completed',now()),
 ('stale',1,'grpc',now()-interval '8 days','running',now()-interval '8 days'),
 ('active',1,'grpc',now()-interval '8 days','running',now()),
 ('recent',1,'grpc',now(),'completed',now())`)
	removed, err := a.pruneConnectivityData(ctx)
	if err != nil || removed != 7 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	for _, table := range []string{"node_ip_pool", "ip_results", "node_scan_rounds"} {
		var count int
		if err := a.db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 2 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var failures int
	if err := a.db.QueryRow(ctx, "SELECT count(*) FROM node_ip_pool WHERE NOT available").Scan(&failures); err != nil || failures != 1 {
		t.Fatalf("current failure lost: count=%d err=%v", failures, err)
	}
	if removed, err = a.pruneConnectivityData(ctx); err != nil || removed != 0 {
		t.Fatalf("repeated cleanup: removed=%d err=%v", removed, err)
	}
}
