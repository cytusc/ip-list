package main

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// A deadline can pass before the cancellation timer gets CPU time.
type delayedDeadlineContext struct {
	context.Context
	at time.Time
}

func (c delayedDeadlineContext) Deadline() (time.Time, bool) { return c.at, true }

func TestDNSNodeDeadlineBeforeCancellation(t *testing.T) {
	ctx := delayedDeadlineContext{context.Background(), time.Now().Add(30 * time.Millisecond)}
	items := []dnsCandidate{{IP: "1.1.1.1", State: "healthy"}, {IP: "8.8.8.8", State: "healthy"}}
	control := make(chan struct{})
	var once sync.Once
	states := dnsCheckNodesWith(ctx, []map[string]any{{"type": "vless", "network": "grpc"}}, items,
		func(_ context.Context, _ map[string]any, ip netip.Addr) (*certificateCheck, error) {
			if ip.String() == "1.1.1.1" {
				once.Do(func() { close(control) })
				return &certificateCheck{Status: "passed"}, nil
			}
			<-control
			time.Sleep(time.Until(ctx.at) + time.Millisecond)
			return &certificateCheck{Status: "error"}, context.DeadlineExceeded
		})
	if states[0] != "passed" || states[1] != "" || items[1].State != "unknown" {
		t.Fatal("checker deadline became an IP failure", states, items)
	}
}

func TestDNSNodeRecheck(t *testing.T) {
	for _, tc := range []struct {
		name     string
		outcomes map[string][]string
		want     map[string]string
	}{
		{"expired evidence", map[string][]string{"38.59.225.75": {"ok", "ok"}, "38.59.225.138": {"invalid"}}, map[string]string{"38.59.225.75": "healthy", "38.59.225.138": "failed"}},
		{"certificate failure without control", map[string][]string{"38.59.225.75": {"invalid"}, "38.59.225.138": {"invalid"}}, map[string]string{"38.59.225.75": "failed", "38.59.225.138": "failed"}},
		{"confirmed transport failure", map[string][]string{"38.59.225.75": {"ok", "ok"}, "38.59.225.138": {"timeout", "timeout"}}, map[string]string{"38.59.225.75": "healthy", "38.59.225.138": "failed"}},
		{"intermittent failure", map[string][]string{"38.59.225.75": {"ok", "ok"}, "38.59.225.138": {"timeout", "ok"}}, map[string]string{"38.59.225.75": "healthy", "38.59.225.138": "unknown"}},
		{"checker outage", map[string][]string{"38.59.225.75": {"timeout", "timeout"}, "38.59.225.138": {"timeout", "timeout"}}, map[string]string{"38.59.225.75": "unknown", "38.59.225.138": "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []dnsCandidate{{IP: "38.59.225.75", State: "healthy", Delay: 164}, {IP: "38.59.225.138", State: "healthy", Delay: 165}, {IP: "1.1.1.1", Pool: "cf", State: "healthy"}, {IP: "8.8.8.8", State: "failed"}}
			var mu sync.Mutex
			calls := map[string]int{}
			check := func(ctx context.Context, template map[string]any, ip netip.Addr) (*certificateCheck, error) {
				mu.Lock()
				defer mu.Unlock()
				if template["servername"] != "gb.062101.xyz" {
					t.Error("lost actual node SNI")
				}
				if len(tc.outcomes[ip.String()]) <= calls[ip.String()] {
					t.Errorf("unexpected check for %s", ip)
					return nil, errors.New("unexpected check")
				}
				outcome := tc.outcomes[ip.String()][calls[ip.String()]]
				calls[ip.String()]++
				switch outcome {
				case "ok":
					return &certificateCheck{Status: "passed"}, nil
				case "invalid":
					return &certificateCheck{Status: "invalid"}, errors.New("hostname mismatch")
				default:
					return nil, context.DeadlineExceeded
				}
			}
			dnsCheckNodesWith(context.Background(), []map[string]any{{"servername": "gb.062101.xyz"}}, items, check)
			for _, item := range items[:2] {
				if item.State != tc.want[item.IP] || calls[item.IP] != len(tc.outcomes[item.IP]) {
					t.Fatalf("%+v calls=%v", items, calls)
				}
			}
			if items[0].Delay != 164 || items[2].State != "healthy" || items[3].State != "failed" || calls["1.1.1.1"] != 0 || calls["8.8.8.8"] != 0 {
				t.Fatal("modified latency, website pool or failed candidate", items)
			}
		})
	}
}

func TestDNSNodeChecksPrioritizeUnfinishedIPs(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	if _, err := a.db.Exec(ctx, `INSERT INTO node_ip_health(ip,samples_hash,status,checked_at) VALUES
 ('1.1.1.1','','passed',now()),('8.8.8.8','','passed',now()-interval '10 minutes')`); err != nil {
		t.Fatal(err)
	}
	items := []dnsCandidate{{IP: "1.1.1.1", State: "healthy"}, {IP: "8.8.8.8", State: "healthy"}, {IP: "9.9.9.9", State: "healthy"}}
	if err := a.dnsCheckNodes(ctx, items); err != nil {
		t.Fatal(err)
	}
	if items[0].IP != "9.9.9.9" || items[1].IP != "8.8.8.8" || items[2].IP != "1.1.1.1" {
		t.Fatal("completed IPs starved unfinished checks", items)
	}
}

func TestDNSNodesRequireEachSample(t *testing.T) {
	// HK2 success cannot corroborate a failed EUGame probe.
	templates := []map[string]any{{"servername": "hk.example"}, {"servername": "gb.example"}}
	items := []dnsCandidate{{IP: "38.59.225.75", State: "healthy"}, {IP: "38.59.225.138", State: "healthy"}}
	dnsCheckNodesWith(context.Background(), templates, items, func(ctx context.Context, template map[string]any, ip netip.Addr) (*certificateCheck, error) {
		if template["servername"] == "gb.example" {
			return nil, context.DeadlineExceeded
		}
		return &certificateCheck{Status: "passed"}, nil
	})
	for _, item := range items {
		if item.State != "unknown" {
			t.Fatal("another SNI counted as a healthy control", items)
		}
	}
	// A working sample must not mask a certificate failure for another SNI.
	dnsCheckNodesWith(context.Background(), templates, items, func(ctx context.Context, template map[string]any, ip netip.Addr) (*certificateCheck, error) {
		if template["servername"] == "gb.example" && ip.String() == "38.59.225.138" {
			return &certificateCheck{Status: "invalid"}, errors.New("hostname mismatch")
		}
		return &certificateCheck{Status: "passed"}, nil
	})
	if items[0].State != "unknown" || items[1].State != "failed" {
		t.Fatal("promoted missing latency evidence or ignored wrong certificate", items)
	}
}

func TestDNSNodesUnknownEvidence(t *testing.T) {
	for _, noTemplates := range []bool{false, true} {
		items := []dnsCandidate{{IP: "38.59.225.75", State: "healthy"}}
		templates := []map[string]any{{"servername": "gb.example"}}
		ctx, cancel := context.WithCancel(context.Background())
		if noTemplates {
			templates = nil
		} else {
			cancel()
		}
		states := dnsCheckNodesWith(ctx, templates, items, func(context.Context, map[string]any, netip.Addr) (*certificateCheck, error) {
			t.Fatal("called after cancellation or without templates")
			return nil, nil
		})
		cancel()
		if items[0].State != "unknown" || states[0] != "" {
			t.Fatal("unstarted checks must not refresh proof age", items, states)
		}
	}
}

func TestDNSNodesRemoveWrongCertificateFromDomain(t *testing.T) {
	items := []dnsCandidate{{IP: "38.59.225.75", Country: "GB", State: "healthy", Delay: 164}, {IP: "38.59.225.138", Country: "GB", State: "healthy", Delay: 165}}
	dnsCheckNodesWith(context.Background(), []map[string]any{{"servername": "gb.example"}}, items, func(ctx context.Context, template map[string]any, ip netip.Addr) (*certificateCheck, error) {
		if ip.String() == "38.59.225.138" {
			return &certificateCheck{Status: "invalid"}, errors.New("hostname mismatch")
		}
		return &certificateCheck{Status: "passed"}, nil
	})
	dnsSelectLatency(items)
	plan := dnsBuildPlan(dnsTestConfig(), items, []dnsRemoteRecord{{ID: "gb", Name: "gb.reiiv.com", Type: "A", Line: "default_view", Status: "ACTIVE", TTL: 300, Values: []string{"38.59.225.75", "38.59.225.138"}}}, map[string]bool{"gb.reiiv.com/A": true}, false)
	if len(plan) != 1 || plan[0].Name != "gb.reiiv.com" || plan[0].Action != "update" || !slices.Equal(plan[0].Desired, []string{"38.59.225.75"}) {
		t.Fatal(plan)
	}
}

func TestDNSNodePublishGate(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	_, err := a.db.Exec(ctx, `
 INSERT INTO nodes(name,address,port,share_uri) VALUES('grpc-sample','sample.invalid',443,'vless://test@sample.invalid:443?type=grpc&security=tls');
 INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at,config_hash,certificate)
 SELECT 'grpc',ip::inet,true,40,now(),md5('vless://test@sample.invalid:443?type=grpc&security=tls'),'{"status":"passed"}'::jsonb
 FROM unnest(ARRAY['38.59.225.75','38.59.225.138']) ip;
 INSERT INTO dns_ip_locations(ip,country,policy_version) SELECT ip::inet,'GB',3 FROM unnest(ARRAY['38.59.225.75','38.59.225.138']) ip;
 INSERT INTO latency_probes(name,token_hash,last_seen,heartbeat_at) VALUES('selected','selected',now(),now());
 INSERT INTO latency_probe_results(probe_id,ip,success,delay_ms,checked_at)
 SELECT id,ip::inet,true,165,now()-make_interval(secs=>age) FROM latency_probes
 CROSS JOIN unnest(ARRAY['38.59.225.75','38.59.225.138']) ip CROSS JOIN generate_series(1,121,20) age;
 INSERT INTO dns_managed_records(domain,zone_id,name,type) VALUES('reiiv.com','dns-test-zone','gb.reiiv.com','A');`)
	if err != nil {
		t.Fatal(err)
	}
	verifyPoolFixture(t, a.db)
	fake := &dnsFakeProvider{records: []dnsRemoteRecord{{ID: "gb", Name: "gb.reiiv.com", Type: "A", Line: "default_view", Status: "ACTIVE", TTL: 300, Values: []string{"38.59.225.75", "38.59.225.138"}}}}
	for _, mode := range []string{"preview", "sync"} {
		called := false
		check := func(ctx context.Context, items []dnsCandidate) error {
			called = true
			dnsCheckNodesWith(ctx, []map[string]any{{"servername": "gb.example"}}, items, func(ctx context.Context, template map[string]any, ip netip.Addr) (*certificateCheck, error) {
				if ip.String() == "38.59.225.138" {
					return &certificateCheck{Status: "invalid"}, errors.New("hostname mismatch")
				}
				return &certificateCheck{Status: "passed"}, nil
			})
			return nil
		}
		report := dnsReport{Mode: mode}
		if err := a.executeDNSWithNodeCheck(ctx, dnsTestConfig(), &report, fake, check); err != nil {
			t.Fatal(err)
		}
		if !called || len(report.Records) != 1 || report.Records[0].Action != "update" || !slices.Equal(report.Records[0].Desired, []string{"38.59.225.75"}) {
			t.Fatalf("mode=%s report=%+v", mode, report)
		}
		if mode == "preview" && len(fake.writes) != 0 || mode == "sync" && len(fake.writes) != 1 {
			t.Fatal("unexpected cloud writes", fake.writes)
		}
	}
	// Invalid sample configuration aborts before any cloud write.
	err = a.dnsCheckNodes(ctx, []dnsCandidate{{IP: "38.59.225.75", State: "healthy"}})
	if err == nil {
		t.Fatal("accepted invalid gRPC sample")
	}
}

func TestDNSUnknownProxyCannotRestoreRejectedProbeIP(t *testing.T) {
	items := []dnsCandidate{{IP: "1.1.1.1", State: "pending", Reason: "success ratio too low"}}
	template := map[string]any{"type": "vless", "network": "ws", "uuid": "private-uuid", "password": "private-password", "ws-opts": map[string]any{"v2ray-http-upgrade": true}}
	dnsCheckNodesWith(context.Background(), []map[string]any{template}, items, func(context.Context, map[string]any, netip.Addr) (*certificateCheck, error) {
		return nil, errors.New("proxy failure private-uuid private-password")
	})
	if items[0].State != "pending" {
		t.Fatal("unknown check preserved rejected IP", items)
	}
	if len(items[0].HealthAttempts) != 2 {
		t.Fatal("attempt history missing", items)
	}
	for _, attempt := range items[0].HealthAttempts {
		if strings.Contains(attempt.Error, "private-") || attempt.Protocol != "vless/httpupgrade" {
			t.Fatal("diagnostics leaked credentials or lost transport", attempt)
		}
	}
}

func TestDNSNodePartialBudgetDoesNotResetRecovery(t *testing.T) {
	for _, negative := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		items := []dnsCandidate{{IP: "8.8.8.8", State: "healthy"}}
		firstDone := make(chan struct{})
		calls := 0
		states := dnsCheckNodesWith(ctx, []map[string]any{{"network": "grpc"}, {"network": "ws"}}, items,
			func(ctx context.Context, template map[string]any, _ netip.Addr) (*certificateCheck, error) {
				if template["network"] == "grpc" {
					calls++
					if calls == 2 {
						close(firstDone)
					}
					if negative && calls == 1 {
						return nil, errors.New("completed connection failure")
					}
					return &certificateCheck{Status: "passed"}, nil
				}
				<-firstDone
				<-ctx.Done()
				return nil, ctx.Err()
			})
		cancel()
		want := ""
		if negative {
			want = "unknown"
		}
		if states[0] != want || items[0].State != "unknown" {
			t.Fatalf("negative=%v states=%v items=%+v", negative, states, items)
		}
	}
}

func TestDNSReuseUnfinishedNodeHealth(t *testing.T) {
	a := paginationApp(t)
	ctx := context.Background()
	_, err := a.db.Exec(ctx, `INSERT INTO nodes(name,address,share_uri) VALUES
 ('sample','sample.example','vless://11111111-1111-4111-8111-111111111111@sample.example:443?type=grpc&security=tls')`)
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	if err = a.db.QueryRow(ctx, `SELECT md5(string_agg(config_hash,',' ORDER BY config_hash)) FROM node_proxy_samples`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, status, state string
		ready, eligible     bool
		age                 time.Duration
		hash                string
		want                bool
	}{
		{"fresh complete proof", "passed", "unknown", true, true, time.Minute, hash, true},
		{"expired proof", "passed", "unknown", true, true, 16 * time.Minute, hash, false},
		{"old sample set", "passed", "unknown", true, true, time.Minute, "old", false},
		{"still recovering", "passed", "unknown", false, true, time.Minute, hash, false},
		{"failed proof", "failed", "unknown", true, true, time.Minute, hash, false},
		{"unknown proof", "unknown", "unknown", true, true, time.Minute, hash, false},
		{"latency rejected", "passed", "pending", true, true, time.Minute, hash, false},
		{"not originally eligible", "passed", "unknown", true, false, time.Minute, hash, false},
		{"new connection failure", "passed", "unknown", true, true, time.Minute, hash, false},
		{"wrong certificate", "passed", "failed", true, true, time.Minute, hash, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Now().UTC().Add(-tc.age)
			_, err := a.db.Exec(ctx, `INSERT INTO node_ip_health(ip,samples_hash,status,checked_at) VALUES('8.8.8.8',$1,$2,$3)
   ON CONFLICT(ip) DO UPDATE SET samples_hash=excluded.samples_hash,status=excluded.status,checked_at=excluded.checked_at`, tc.hash, tc.status, at)
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.db.Exec(ctx, `INSERT INTO ip_health_recovery(pool,ip,samples_hash,checked_at,ready,state) VALUES('node','8.8.8.8',$1,$2,$3,'{}')
   ON CONFLICT(pool,ip) DO UPDATE SET samples_hash=excluded.samples_hash,checked_at=excluded.checked_at,ready=excluded.ready`, tc.hash, at, tc.ready)
			if err != nil {
				t.Fatal(err)
			}
			states := []string{""}
			if tc.name == "new connection failure" {
				states[0] = "unknown"
			}
			if tc.name == "wrong certificate" {
				states[0] = "failed"
			}
			items := []dnsCandidate{{IP: "8.8.8.8", State: tc.state}}
			if err = a.reuseUnfinishedNodeHealth(ctx, hash, items, states, map[string]bool{"8.8.8.8": tc.eligible}); err != nil {
				t.Fatal(err)
			}
			if (items[0].State == "healthy") != tc.want {
				t.Fatalf("unexpected admission: %+v", items)
			}
			var saved time.Time
			if err = a.db.QueryRow(ctx, `SELECT checked_at FROM node_ip_health WHERE ip='8.8.8.8'`).Scan(&saved); err != nil || saved.Sub(at).Abs() > time.Microsecond {
				t.Fatalf("reused proof timestamp changed: %s %v", saved, err)
			}
		})
	}
}
