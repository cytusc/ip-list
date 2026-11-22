package ipguard

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
	exec(`INSERT INTO engine_jobs(job_id,kind,status,created_at,finished_at) VALUES
 ('old-done','scan','done',now()-interval '8 days',now()-interval '8 days'),
 ('old-failed','scan','failed',now()-interval '8 days',now()-interval '8 days'),
 ('recent-done','scan','done',now()-interval '1 day',now()),
 ('active-job','scan','running',now()-interval '8 days',NULL)`)
	exec(`INSERT INTO node_scan_rounds(job_id,round,protocol,started_at,status,updated_at) VALUES
 ('expired',1,'grpc',now()-interval '8 days','completed',now()),
 ('stale',1,'grpc',now()-interval '8 days','running',now()-interval '8 days'),
 ('active',1,'grpc',now()-interval '8 days','running',now()),
 ('recent',1,'grpc',now(),'completed',now())`)
	removed, err := a.pruneConnectivityData(ctx)
	if err != nil || removed != 7 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	for table, want := range map[string]int{"node_ip_pool": 2, "node_scan_rounds": 2, "engine_jobs": 2} {
		var count int
		if err := a.db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
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
