package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProbeCredentialEncryption(t *testing.T) {
	t.Setenv("PROBE_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	token, _, _ := newProbeToken()
	first, err := encryptProbeToken(token)
	if err != nil {
		t.Fatal(err)
	}
	second, err := encryptProbeToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) || bytes.Contains(first, []byte(token)) {
		t.Fatal("ciphertext must use unique nonces and hide token")
	}
	restored, err := decryptProbeToken(first)
	if err != nil || restored != token {
		t.Fatal("roundtrip failed")
	}
	first[len(first)-1] ^= 1
	if _, err = decryptProbeToken(first); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	t.Setenv("PROBE_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)))
	if _, err = decryptProbeToken(second); err == nil {
		t.Fatal("wrong key accepted")
	}
	t.Setenv("PROBE_CREDENTIAL_KEY", "")
	if _, err = encryptProbeToken(token); err == nil {
		t.Fatal("missing encryption key accepted")
	}
}

func TestProbeDeploymentPersistenceAndLegacyMigration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires TEST_DATABASE_URL")
	}
	t.Setenv("PROBE_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
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
	create := httptest.NewRecorder()
	a.createLatencyProbe(create, httptest.NewRequest("POST", "/latency-probes", strings.NewReader(`{"name":"credential-`+time.Now().Format("150405.000000000")+`","interval_seconds":60,"timeout_ms":1000,"concurrency":2,"enabled":true}`)))
	if create.Code != 201 {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	var created struct {
		Probe latencyProbe `json:"probe"`
		Token string       `json:"token"`
	}
	if err = json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DELETE FROM latency_probes WHERE id=$1", created.Probe.ID)
	deployment := func(method, body string) (int, map[string]string) {
		t.Helper()
		req := httptest.NewRequest(method, "/deployment", strings.NewReader(body))
		req.SetPathValue("id", created.Probe.ID)
		response := httptest.NewRecorder()
		a.probeDeployment(response, req)
		var data map[string]string
		if err = json.Unmarshal(response.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("credentials may be cached")
		}
		return response.Code, data
	}
	verify := func(token, platform string) {
		t.Helper()
		code, data := deployment("GET", "")
		if code != 200 || data["token"] != token || data["platform"] != platform {
			t.Fatal("persisted deployment mismatch")
		}
	}
	verify(created.Token, "darwin")
	code, _ := deployment("PUT", `{"token":"`+created.Token+`","platform":"linux"}`)
	if code != 200 {
		t.Fatal("save failed")
	}
	verify(created.Token, "linux")
	code, _ = deployment("PUT", `{"token":"wrong-token","platform":"windows"}`)
	if code != 422 {
		t.Fatal("wrong token accepted")
	}
	verify(created.Token, "linux")
	req := httptest.NewRequest(http.MethodGet, "/targets", nil)
	req.Header.Set("Authorization", "Bearer "+created.Token)
	if _, ok := a.authorizeLatencyProbe(req); !ok {
		t.Fatal("copy/save changed authentication")
	}
	// Simulate a legacy hash-only row and recover without rotating the token.
	if _, err = db.Exec(ctx, "UPDATE latency_probes SET token_encrypted=NULL WHERE id=$1", created.Probe.ID); err != nil {
		t.Fatal(err)
	}
	verify("", "linux")
	if _, ok := a.authorizeLatencyProbe(req); !ok {
		t.Fatal("legacy authentication failed")
	}
	verify(created.Token, "linux")
	listing := httptest.NewRecorder()
	a.listLatencyProbes(listing, httptest.NewRequest("GET", "/latency-probes", nil))
	if strings.Contains(listing.Body.String(), created.Token) || strings.Contains(listing.Body.String(), "token_encrypted") {
		t.Fatal("list leaks credentials")
	}
	rotateReq := httptest.NewRequest("POST", "/token", nil)
	rotateReq.SetPathValue("id", created.Probe.ID)
	rotated := httptest.NewRecorder()
	a.rotateLatencyProbeToken(rotated, rotateReq)
	var rotation map[string]string
	json.Unmarshal(rotated.Body.Bytes(), &rotation)
	if rotated.Code != 200 || rotation["token"] == created.Token {
		t.Fatal("manual rotation failed")
	}
	verify(rotation["token"], "linux")
	if _, ok := a.authorizeLatencyProbe(req); ok {
		t.Fatal("old token still valid after explicit rotation")
	}
	req.Header.Set("Authorization", "Bearer "+rotation["token"])
	if _, ok := a.authorizeLatencyProbe(req); !ok {
		t.Fatal("new token invalid")
	}
}
