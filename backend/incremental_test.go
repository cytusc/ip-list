package main

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIncrementalTargets(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		cidrs, checked, want []string
	}{
		{"new network", []string{"192.0.2.0/31", "192.0.2.2/31"}, []string{"192.0.2.0", "192.0.2.1"}, []string{"192.0.2.2", "192.0.2.3"}},
		{"overlap and unfinished addresses", []string{"192.0.2.0/30", "192.0.2.0/31"}, []string{"192.0.2.0", "192.0.2.2"}, []string{"192.0.2.1", "192.0.2.3"}},
		{"IPv6", []string{"2001:db8::/126"}, []string{"2001:db8::", "2001:db8::1"}, []string{"2001:db8::2", "2001:db8::3"}},
		{"all checked", []string{"192.0.2.0/31"}, []string{"192.0.2.0", "192.0.2.1"}, nil},
		{"first scan", []string{"192.0.2.0/31"}, nil, []string{"192.0.2.0", "192.0.2.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total, targets, err := scanTargetsExcluding(tc.cidrs, tc.checked)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for ip := range targets {
				got = append(got, ip.String())
			}
			if total != len(tc.want) || !slices.Equal(got, tc.want) {
				t.Fatalf("total=%d targets=%v want=%v", total, got, tc.want)
			}
		})
	}
	// Subtracting an address from a large range must remain lazy.
	total, targets, err := scanTargetsExcluding([]string{"0.0.0.0/0"}, []string{"0.0.0.0"})
	if err != nil || total != 4294967295 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	for ip := range targets {
		if ip != netip.MustParseAddr("0.0.0.1") {
			t.Fatal(ip)
		}
		break
	}
}

func TestIncrementalPoolIsolation(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("设置 TEST_DATABASE_URL 可验证增量检测池隔离")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !strings.HasSuffix(db.Config().ConnConfig.Database, "_test") {
		t.Fatal("测试数据库名必须以 _test 结尾")
	}
	if _, err = db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	protocol := fmt.Sprintf("incremental/%d", time.Now().UnixNano())
	defer func() { _, _ = db.Exec(ctx, "DELETE FROM node_ip_pool WHERE protocol=$1", protocol) }()
	_, err = db.Exec(ctx, `INSERT INTO node_ip_pool(protocol,ip,available,delay_ms,checked_at) VALUES
 ($1,'192.0.2.0',true,1,now()),($1,'192.0.2.1',false,0,now()),($1,'198.51.100.0',true,1,now())`, protocol)
	if err != nil {
		t.Fatal(err)
	}
	attachPoolConfig(t, db, protocol)
	a := &app{db: db}
	checked, err := a.checkedScanIPs(ctx, []string{"192.0.2.0/30"}, protocol)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(checked)
	if !slices.Equal(checked, []string{"192.0.2.0", "192.0.2.1"}) {
		t.Fatalf("checked=%v", checked)
	}
	checked, err = a.checkedScanIPs(ctx, []string{"192.0.2.0/30"}, protocol+"x")
	if err != nil || len(checked) != 0 {
		t.Fatalf("other pool: checked=%v err=%v", checked, err)
	}
	// The CF query must use its independent pool, regardless of sample domain.
	var expected int
	if err = db.QueryRow(ctx, "SELECT count(*) FROM cf_ip_pool WHERE ip <<= '192.0.2.0/30'::cidr").Scan(&expected); err != nil {
		t.Fatal(err)
	}
	checked, err = a.checkedScanIPs(ctx, []string{"192.0.2.0/30"}, "")
	if err != nil || len(checked) != expected {
		t.Fatalf("CF pool: checked=%v expected=%d err=%v", checked, expected, err)
	}
}
