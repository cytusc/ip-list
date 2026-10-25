package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProtocolPoolAndEmptyRanges(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("requires TEST_DATABASE_URL")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !strings.HasSuffix(db.Config().ConnConfig.Database, "_test") {
		t.Fatal("requires test database")
	}
	if _, err = db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db}
	ids := []string{}
	networkID := ""
	defer func() {
		for _, id := range ids {
			db.Exec(ctx, "DELETE FROM nodes WHERE id=$1", id)
		}
		db.Exec(ctx, "DELETE FROM networks WHERE id=$1", networkID)
		db.Exec(ctx, "DELETE FROM node_ip_pool WHERE ip <<= '198.18.91.0/24'")
	}()
	createNode := func(transport string) string {
		t.Helper()
		var id string
		raw := fmt.Sprintf("vless://%s@sample.invalid:443?security=tls&type=%s", testUUID, transport)
		if err := db.QueryRow(ctx, "INSERT INTO nodes(name,address,port,share_uri) VALUES($1,'sample.invalid',443,$2) RETURNING id::text", fmt.Sprintf("pool-%d", time.Now().UnixNano()), raw).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		return id
	}
	first := createNode("grpc")
	second := createNode("grpc")
	other := createNode("xhttp")
	if err = db.QueryRow(ctx, "INSERT INTO networks(name,cidr) VALUES('pool-range','198.18.91.0/24') RETURNING id::text").Scan(&networkID); err != nil {
		t.Fatal(err)
	}
	var raw string
	db.QueryRow(ctx, "SELECT share_uri FROM nodes WHERE id=$1", first).Scan(&raw)
	hash := md5.Sum([]byte(raw))
	now := time.Now().UTC()
	job := scanJob{Protocol: "vless/grpc", ConfigHash: hex.EncodeToString(hash[:]), Input: scanInput{NodeID: first, NetworkIDs: []string{networkID}}, Scope: "full", Status: "completed", CIDRs: []string{"198.18.91.0/24"}, Total: 256, Completed: 256, FinishedAt: &now}
	for i := 0; i < 256; i++ {
		job.Results = append(job.Results, scanResult{IP: fmt.Sprintf("198.18.91.%d", i), Status: "failed"})
	}
	save := func() {
		t.Helper()
		if err := a.saveIPResults(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	disabled := func(protocol string) bool {
		t.Helper()
		cidrs, err := a.protocolRanges(ctx, []string{networkID}, protocol)
		if err != nil {
			t.Fatal(err)
		}
		return len(cidrs) == 0
	}
	// A canceled round, partial coverage, and an available-only round cannot disable the /24.
	job.Status = "cancelled"
	save()
	if disabled(job.Protocol) {
		t.Fatal("cancelled round disabled range")
	}
	job.Status = "completed"
	job.Completed = 255
	job.Results = job.Results[:255]
	save()
	if disabled(job.Protocol) {
		t.Fatal("partial coverage disabled range")
	}
	job.Results = append(job.Results, scanResult{IP: "198.18.91.255", Status: "failed"})
	job.Completed = 256
	job.Scope = "available"
	save()
	if disabled(job.Protocol) {
		t.Fatal("retest disabled range")
	}
	job.Scope = "full"
	job.Results[5].Status = "success"
	save()
	if disabled(job.Protocol) {
		t.Fatal("range with success disabled")
	}
	links := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		a.generateIPLinks(w, httptest.NewRequest("GET", "/available-ips/links?ip=198.18.91.5", nil))
		var items []generatedLink
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &items) != nil || len(items) != want {
			t.Fatalf("links %d: %s", w.Code, w.Body.String())
		}
		for _, link := range items {
			if link.NodeID == other {
				t.Fatal("protocol isolation failed")
			}
		}
	}
	links(2)
	// A newly added matching node inherits the pool without a scan.
	createNode("grpc")
	links(3)
	db.Exec(ctx, "DELETE FROM nodes WHERE id=$1", first)
	links(2)
	db.Exec(ctx, "DELETE FROM nodes WHERE id=$1", second)
	links(1)
	// Finished results remain usable even after the sample was deleted.
	later := now.Add(time.Second)
	job.FinishedAt = &later
	job.Results[5].Status = "failed"
	save()
	if !disabled(job.Protocol) || disabled("vless/xhttp") {
		t.Fatal("empty range did not disable only its protocol")
	}
	var enabled bool
	db.QueryRow(ctx, "SELECT enabled FROM networks WHERE id=$1", networkID).Scan(&enabled)
	if !enabled {
		t.Fatal("globally disabled range")
	}
	request := httptest.NewRequest("DELETE", "/protocol-networks/"+networkID+"?protocol=vless%2Fgrpc", nil)
	request.SetPathValue("id", networkID)
	w := httptest.NewRecorder()
	a.restoreProtocolNetwork(w, request)
	if w.Code != 200 || disabled(job.Protocol) {
		t.Fatal("restore failed")
	}
	// Older failed full rounds cannot disable a pool with newer successful evidence.
	newer := later.Add(time.Second)
	job.FinishedAt = &newer
	job.Results[5].Status = "success"
	save()
	job.FinishedAt = &later
	job.Results[5].Status = "failed"
	save()
	if disabled(job.Protocol) {
		t.Fatal("stale full round disabled range")
	}
	// Editing the CIDR invalidates an old per-pool disable entry.
	db.Exec(ctx, "INSERT INTO protocol_networks(protocol,network_id,cidr,checked_at) VALUES($1,$2,'198.18.91.0/24',now())", job.Protocol, networkID)
	db.Exec(ctx, "UPDATE networks SET cidr='198.18.92.0/24' WHERE id=$1", networkID)
	if disabled(job.Protocol) {
		t.Fatal("old CIDR disable followed edited range")
	}
}
