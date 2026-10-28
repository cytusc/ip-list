package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func dnsNotifyTestReport() dnsReport {
	at := time.Date(2026, 10, 28, 2, 30, 0, 0, time.UTC)
	return dnsReport{
		Mode: "sync", Domain: "reiiv.com", StartedAt: at, FinishedAt: &at,
		Records: []dnsPlanRecord{{Name: "hk.reiiv.com", Type: "A", Action: "update", Applied: true,
			Current: []string{"1.1.1.1", "8.8.8.8"}, Desired: []string{"1.1.1.1", "9.9.9.9"}}},
	}
}

func TestDNSIPDifference(t *testing.T) {
	got := dnsIPDifference([]string{"8.8.8.8", "1.1.1.1", "8.8.8.8", "2606:4700:4700:0:0:0:0:1111"}, []string{"2606:4700:4700::1111"})
	if !slices.Equal(got, []string{"1.1.1.1", "8.8.8.8"}) {
		t.Fatal(got)
	}
}

func TestDNSFeishuChangeCard(t *testing.T) {
	report := dnsNotifyTestReport()
	report.Error = "another record failed"
	messages, err := dnsFeishuMessages(report)
	if err != nil || len(messages) != 1 {
		t.Fatal(messages, err)
	}
	raw, _ := json.Marshal(messages[0])
	for _, want := range []string{"interactive", "DNS IP 变动通知", "hk.reiiv.com", "HK", "A · 更新解析", "2026-10-28 10:30:00", "北京时间", "新增 IP（1）", "移除 IP（1）", "`9.9.9.9`", "`8.8.8.8`", "部分同步失败"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("card missing %q: %s", want, raw)
		}
	}
	if strings.Contains(string(raw), "1.1.1.1") {
		t.Fatal("unchanged IP appeared in the diff", string(raw))
	}
}

func TestDNSFeishuSkipsNonChanges(t *testing.T) {
	for _, name := range []string{"preview", "failed write", "hold", "unchanged", "TTL only", "IPv6 spelling", "other subdomain", "other record type"} {
		t.Run(name, func(t *testing.T) {
			report := dnsNotifyTestReport()
			r := &report.Records[0]
			switch name {
			case "preview":
				report.Mode = "preview"
			case "failed write":
				r.Applied = false
			case "hold", "unchanged":
				r.Action = name
			case "TTL only":
				r.Desired = []string{"8.8.8.8", "1.1.1.1"}
			case "IPv6 spelling":
				r.Type = "AAAA"
				r.Current = []string{"2606:4700:4700:0:0:0:0:1111"}
				r.Desired = []string{"2606:4700:4700::1111"}
			case "other subdomain":
				r.Name = "www.reiiv.com"
			case "other record type":
				r.Type = "TXT"
			}
			messages, err := dnsFeishuMessages(report)
			if err != nil || len(messages) != 0 {
				t.Fatal(messages, err)
			}
		})
	}
}

func TestDNSFeishuCreateAndDelete(t *testing.T) {
	report := dnsNotifyTestReport()
	report.Records = []dnsPlanRecord{
		{Name: "us.reiiv.com", Type: "A", Action: "delete", Applied: true, Current: []string{"8.8.8.8"}},
		{Name: "jp.reiiv.com", Type: "AAAA", Action: "create", Applied: true, Desired: []string{"2606:4700:4700::1111"}},
	}
	messages, err := dnsFeishuMessages(report)
	if err != nil || len(messages) != 1 {
		t.Fatal(messages, err)
	}
	raw, _ := json.Marshal(messages)
	for _, want := range []string{"AAAA · 新增解析", "A · 删除解析", "新增 IP（0）", "移除 IP（0）", "`2606:4700:4700::1111`", "`8.8.8.8`"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("card missing %q: %s", want, raw)
		}
	}
	if strings.Index(string(raw), "jp.reiiv.com") > strings.Index(string(raw), "us.reiiv.com") || report.Records[0].Name != "us.reiiv.com" {
		t.Fatal("cards should be sorted without changing the report")
	}
}

func TestDNSFeishuSplitsLargeCards(t *testing.T) {
	report := dnsNotifyTestReport()
	report.Records = nil
	for _, country := range []string{"hk", "us", "jp", "sg", "de", "fr", "gb", "ca", "au", "kr", "tw", "mo"} {
		r := dnsPlanRecord{Name: country + ".reiiv.com", Type: "AAAA", Action: "update", Applied: true}
		for i := 1; i <= 50; i++ {
			r.Current = append(r.Current, fmt.Sprintf("2606:4700:4700:1111:2222:3333:4444:%x", i))
			r.Desired = append(r.Desired, fmt.Sprintf("2606:4700:4700:1111:2222:3333:5555:%x", i))
		}
		report.Records = append(report.Records, r)
	}
	messages, err := dnsFeishuMessages(report)
	if err != nil || len(messages) < 2 {
		t.Fatal("large change was not split", len(messages), err)
	}
	all := ""
	for _, m := range messages {
		raw, err := json.Marshal(m)
		if err != nil || len(raw) > dnsFeishuCardLimit {
			t.Fatal("oversized card", len(raw), err)
		}
		all += string(raw)
	}
	for _, r := range report.Records {
		if strings.Count(all, r.Name) != 1 {
			t.Fatal("record lost or repeated", r.Name)
		}
	}
}

func TestSendDNSFeishu(t *testing.T) {
	messages, err := dnsFeishuMessages(dnsNotifyTestReport())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"success", `{"code":0,"msg":"success"}`, 200, false},
		{"legacy success", `{"StatusCode":0,"StatusMessage":"success"}`, 200, false},
		{"bot rejection", `{"code":19024,"msg":"sensitive-webhook-token"}`, 200, true},
		{"legacy rejection", `{"StatusCode":1}`, 200, true},
		{"rate limit", `{"code":9499}`, 429, true},
		{"invalid JSON", `broken`, 200, true},
		{"missing status", `{}`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("invalid request", r.Method, r.Header)
				}
				var m dnsFeishuMessage
				if err := json.NewDecoder(r.Body).Decode(&m); err != nil || m.MsgType != "interactive" || len(m.Card.Elements) == 0 {
					t.Error("invalid card payload", m, err)
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			err := sendDNSFeishu(context.Background(), server.Client(), server.URL+"/sensitive-webhook-token", messages)
			if (err != nil) != tc.wantError {
				t.Fatal(err)
			}
			if err != nil && strings.Contains(err.Error(), "sensitive-webhook-token") {
				t.Fatal("error exposed the webhook token", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sendDNSFeishu(ctx, http.DefaultClient, "https://example.invalid/sensitive-webhook-token", messages); err == nil || strings.Contains(err.Error(), "sensitive-webhook-token") {
		t.Fatal("canceled request should fail without exposing token", err)
	}
}

func TestNotifyDNSReport(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"code":19024}`)
	}))
	defer server.Close()
	t.Setenv("FEISHU_DNS_WEBHOOK_URL", server.URL+"/secret-token")
	report := dnsNotifyTestReport()
	report.Warning = "existing warning"
	report.Error = "partial cloud failure"
	notifyDNSReport(&report)
	if calls != 1 || !strings.Contains(report.Warning, "existing warning；DNS 变更飞书通知未全部送达") || report.Error != "partial cloud failure" || !report.Records[0].Applied || strings.Contains(report.Warning, "secret-token") {
		t.Fatal(calls, report)
	}
	t.Setenv("FEISHU_DNS_WEBHOOK_URL", "")
	notifyDNSReport(&report)
	if calls != 1 {
		t.Fatal("disabled notifications sent a request")
	}
}
