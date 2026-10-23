package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/metacubex/mihomo/listener/inbound"
	"github.com/redis/go-redis/v9"
)

func TestPatrolSelectionAndChanges(t *testing.T) {
	all := []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")}
	previous := map[string]scanResult{all[0].String(): {Status: "success", Delay: 42}, all[1].String(): {Status: "failed"}}
	for _, round := range []int{1, 13, 25} {
		ips, scope := patrolTargets(all, previous, round, 12)
		if len(ips) != 2 || scope != "full" {
			t.Fatal("full round omitted", round)
		}
	}
	ips, scope := patrolTargets(all, previous, 2, 12)
	if len(ips) != 1 || ips[0] != all[0] || scope != "available" {
		t.Fatal("unexpected retest scope")
	}
	if ips, scope = patrolTargets(all, nil, 2, 12); len(ips) != 2 || scope != "full" {
		t.Fatal("empty usable set did not fall back")
	}
	if ips, scope = patrolTargets(all, previous, 2, 1); len(ips) != 2 || scope != "full" {
		t.Fatal("every-round full mode failed")
	}
	for _, c := range []struct{ ip, status, change string }{
		{"192.0.2.1", "failed", "lost"}, {"192.0.2.2", "success", "recovered"},
		{"192.0.2.3", "success", "new"}, {"192.0.2.3", "failed", ""}, {"192.0.2.1", "success", ""},
	} {
		result := compareResult(scanResult{IP: c.ip, Status: c.status}, previous)
		if result.Change != c.change {
			t.Fatalf("unexpected transition: %+v", result)
		}
		if c.ip == "192.0.2.1" && (result.PreviousDelay == nil || *result.PreviousDelay != 42) {
			t.Fatal("previous delay lost")
		}
	}
}

func TestPatrolRedisLifecycle(t *testing.T) {
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("设置 TEST_REDIS_URL 可运行巡检集成测试")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(options)
	defer rdb.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &app{redis: rdb, scanContext: ctx}
	server, err := inbound.NewVless(&inbound.VlessOption{AllowInsecure: true, BaseOption: inbound.BaseOption{NameStr: "patrol-test", Listen: "127.0.0.1", Port: "0"}, Users: []inbound.VlessUser{{Username: "test", UUID: testUUID}}})
	if err != nil {
		t.Fatal(err)
	}
	tunnel := &probeTestTunnel{}
	tunnel.status.Store(503) // Reachability, not status-code policy.
	if err = server.Listen(tunnel); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	address := netip.MustParseAddrPort(server.Address())
	raw := fmt.Sprintf("vless://%s@original.invalid:%d?security=none&type=tcp", testUUID, address.Port())
	template, _, err := parseShare(raw)
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%032x", time.Now().UnixNano())
	locked, err := rdb.SetNX(ctx, scanLock, id, 45*time.Second).Result()
	if err != nil || !locked {
		t.Fatal("test Redis has an active scan", err)
	}
	defer rdb.Del(context.Background(), scanKey(id), scanKey(id)+":cancel")
	input := scanInput{URL: "http://probe.example/", Timeout: 1000, Concurrency: 2, IntervalMinutes: 1, FullEvery: 12}
	job := scanJob{ID: id, Input: input, Round: 1, Scope: "full", Status: "running", Total: 2, StartedAt: time.Now().UTC()}
	if !a.savePatrol(&job) {
		t.Fatal("initial save failed")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.runPatrol(job, input, template, []netip.Addr{address.Addr(), netip.MustParseAddr("127.0.0.2")}, 500*time.Millisecond)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("patrol did not stop")
		}
	}()
	wait := func(round int) scanJob {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			data, err := rdb.Get(ctx, scanKey(id)).Bytes()
			var current scanJob
			if err == nil && json.Unmarshal(data, &current) == nil && current.Round == round && current.Status == "waiting" {
				return current
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("round %d did not finish", round)
		return scanJob{}
	}
	first := wait(1)
	if first.Total != 2 || first.Success != 1 || first.NextRunAt == nil {
		t.Fatalf("first discovery: %+v", first)
	}
	_ = server.Close()
	second := wait(2)
	if second.Scope != "available" || second.Total != 1 || second.Success != 0 || second.Results[0].Change != "lost" {
		t.Fatalf("retest: %+v", second)
	}
	third := wait(3)
	if third.Scope != "full" || third.Total != 2 {
		t.Fatalf("empty-set recovery: %+v", third)
	}
	if err = rdb.Set(ctx, scanKey(id)+":cancel", "1", scanTTL).Err(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("waiting patrol failed to cancel")
	}
	data, _ := rdb.Get(ctx, scanKey(id)).Bytes()
	if err = json.Unmarshal(data, &job); err != nil || job.Status != "cancelled" || job.NextRunAt != nil {
		t.Fatalf("cancel state: %+v %v", job, err)
	}
	if rdb.Exists(ctx, scanLock).Val() != 0 {
		t.Fatal("lease was not released")
	}
}
