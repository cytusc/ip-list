package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

func TestIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("设置 TEST_DATABASE_URL 和 TEST_REDIS_URL 可运行真实数据库集成测试")
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
	options, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(options)
	defer rdb.Close()
	if _, err = db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	username := fmt.Sprintf("test_%d", time.Now().UnixNano())
	password := "Integration-Password-2026"
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), 12)
	if _, err = db.Exec(ctx, "INSERT INTO users(username,password_hash) VALUES($1,$2)", username, string(hash)); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec(ctx, "DELETE FROM users WHERE username=$1", username) }()
	t.Setenv("APP_ENV", "development")
	t.Setenv("APP_ORIGIN", "http://localhost:3000")
	a, err := newApp(db, rdb)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a.routes())
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	request := func(method, path string, value any, expected int) map[string]any {
		t.Helper()
		body, _ := json.Marshal(value)
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://localhost:3000")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		if response.StatusCode != expected {
			t.Fatalf("%s %s: expected %d, got %d: %s", method, path, expected, response.StatusCode, raw)
		}
		var result map[string]any
		_ = json.Unmarshal(raw, &result)
		return result
	}
	request("GET", "/nodes", nil, 401)
	request("GET", "/scans/latest", nil, 401)
	request("POST", "/auth/login", map[string]string{"username": username, "password": "wrong"}, 401)
	request("POST", "/auth/login", map[string]string{"username": username, "password": password}, 200)
	request("GET", "/auth/me", nil, 200)
	node := map[string]any{"name": username, "address": "node.example.com", "port": 8080, "enabled": true, "description": "integration"}
	created := request("POST", "/nodes", node, 201)
	nodeID := created["id"].(string)
	defer func() {
		_, _ = db.Exec(ctx, "DELETE FROM networks WHERE node_id=$1", nodeID)
		_, _ = db.Exec(ctx, "DELETE FROM nodes WHERE id=$1", nodeID)
	}()
	request("POST", "/nodes", node, 409)
	node["port"] = 0
	request("PUT", "/nodes/"+nodeID, node, 422)
	node["port"], node["enabled"] = 9090, false
	request("PUT", "/nodes/"+nodeID, node, 200)
	request("GET", "/nodes", nil, 200)
	network := map[string]any{"name": "office", "cidr": "192.168.8.1/24", "node_id": nodeID, "action": "deny", "enabled": true, "description": ""}
	request("POST", "/networks", network, 422)
	network["cidr"] = "192.168.8.0/24"
	networkID := request("POST", "/networks", network, 201)["id"].(string)
	request("POST", "/networks", network, 409)
	request("DELETE", "/nodes/"+nodeID, nil, 409)

	// Multiple transports at the same endpoint must import atomically.
	firstURI := "vless://" + testUUID + "@same.example:443?security=tls&type=grpc&serviceName=svc#" + username + "-grpc"
	secondURI := "vless://" + testUUID + "@same.example:443?security=tls&type=xhttp&mode=packet-up#" + username + "-xhttp"
	request("POST", "/nodes/import", map[string]string{"links": firstURI + "\ninvalid"}, 422)
	var count int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM nodes WHERE name=$1", username+"-grpc").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial import", err)
	}
	request("POST", "/nodes/import", map[string]string{"links": firstURI + "\n" + secondURI}, 201)
	defer func() {
		_, _ = db.Exec(ctx, "DELETE FROM nodes WHERE name=ANY($1)", []string{username + "-grpc", username + "-xhttp"})
	}()

	// A port accepting TCP but never completing VLESS must not count as success.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	node["share_uri"] = "vless://" + testUUID + "@original.invalid:" + fmt.Sprint(listener.Addr().(*net.TCPAddr).Port) + "?security=none&type=tcp"
	node["enabled"] = true
	request("PUT", "/nodes/"+nodeID, node, 200)
	network["cidr"] = "127.0.0.0/26"
	request("PUT", "/networks/"+networkID, network, 200)
	scanInput := map[string]any{"node_id": nodeID, "network_ids": []string{networkID}, "url": "http://probe.example/generate_204", "timeout_ms": 5000, "concurrency": 1}
	scanID := request("POST", "/scans", scanInput, 202)["id"].(string)
	request("POST", "/scans", scanInput, 409)
	if request("GET", "/scans/latest", nil, 200)["id"] != scanID {
		t.Fatal("latest job lost")
	}
	request("POST", "/scans/"+scanID+"/cancel", nil, 200)
	waitScan := func(id string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			result := request("GET", "/scans/"+id, nil, 200)
			if result["status"] != "running" {
				return result
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("scan did not finish")
		return nil
	}
	cancelled := waitScan(scanID)
	if cancelled["status"] != "cancelled" || cancelled["success"] != float64(0) || cancelled["completed"].(float64) >= 64 {
		t.Fatalf("cancellation failed: %v", cancelled)
	}
	// Worker closes and releases the lease before the next job.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if rdb.Exists(ctx, scanLock).Val() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	network["cidr"] = "127.0.0.1/32"
	request("PUT", "/networks/"+networkID, network, 200)
	scanInput["timeout_ms"] = 1000
	scanID = request("POST", "/scans", scanInput, 202)["id"].(string)
	completed := waitScan(scanID)
	if completed["status"] != "completed" || completed["success"] != float64(0) || completed["completed"] != float64(1) {
		t.Fatal("TCP incorrectly passed", completed)
	}
	network["cidr"] = "2001:db8::/32"
	request("PUT", "/networks/"+networkID, network, 200)
	request("POST", "/scans", scanInput, 422)

	network["cidr"], network["action"] = "2001:db8::/32", "allow"
	request("PUT", "/networks/"+networkID, network, 200)
	request("GET", "/networks", nil, 200)
	request("DELETE", "/networks/"+networkID, nil, 200)
	request("DELETE", "/nodes/"+nodeID, nil, 200)
	request("DELETE", "/nodes/"+nodeID, nil, 404)
	req, _ := http.NewRequest("POST", server.URL+"/nodes", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://untrusted.example")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("跨站请求未被拒绝")
	}
	request("POST", "/auth/logout", nil, 200)
	request("GET", "/auth/me", nil, 401)
	for i := 0; i < 10; i++ {
		request("POST", "/auth/login", map[string]string{"username": username, "password": "wrong"}, 401)
	}
	request("POST", "/auth/login", map[string]string{"username": username, "password": password}, 429)
}
