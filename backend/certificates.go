package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/metacubex/mihomo/component/dialer"
)

const certificateAttempts = 3

type certificateCheck struct {
	Status     string     `json:"status"`
	ServerName string     `json:"server_name"`
	Subject    string     `json:"subject,omitempty"`
	Issuer     string     `json:"issuer,omitempty"`
	DNSNames   []string   `json:"dns_names,omitempty"`
	NotAfter   *time.Time `json:"not_after,omitempty"`
	CheckedAt  time.Time  `json:"checked_at"`
	Attempts   int        `json:"attempts"`
	Error      string     `json:"error,omitempty"`
}

// Each attempt opens a new direct connection to the candidate IP, retaining the
// node's original SNI. Session resumption is disabled so every attempt checks a
// fresh certificate chain. The scan's per-IP deadline bounds all three attempts.
func checkCertificate(ctx context.Context, template map[string]any, ip netip.Addr) (*certificateCheck, error) {
	return checkCertificateWithRoots(ctx, template, ip, nil)
}

func checkCertificateWithRoots(ctx context.Context, template map[string]any, ip netip.Addr, roots *x509.CertPool) (*certificateCheck, error) {
	result := &certificateCheck{Status: "error", CheckedAt: time.Now().UTC()}
	result.ServerName, _ = template["servername"].(string)
	if template["tls"] != true {
		result.Status = "not_required"
		return result, nil
	}
	if result.ServerName == "" {
		result.Status = "invalid"
		result.Error = "TLS 节点未配置 SNI，无法验证入口证书"
		return result, errors.New(result.Error)
	}
	port, err := strconv.Atoi(fmt.Sprint(template["port"]))
	if err != nil || port < 1 || port > 65535 {
		return result, errors.New("无效的 TLS 端口")
	}
	var options []dialer.Option
	if iface := env("PROBE_INTERFACE", ""); iface != "" {
		options = append(options, dialer.WithInterface(iface))
	}
	alpn := []string(nil)
	switch value := template["alpn"].(type) {
	case []string:
		alpn = value
	case []any:
		for _, v := range value {
			if s, ok := v.(string); ok {
				alpn = append(alpn, s)
			}
		}
	}
	for i := 0; i < certificateAttempts; i++ {
		result.Attempts++
		raw, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)), options...)
		if err != nil {
			result.Error = "入口 TLS 连接失败或超时"
			return result, err
		}
		conn := tls.Client(raw, &tls.Config{ServerName: result.ServerName, RootCAs: roots, NextProtos: alpn, MinVersion: tls.VersionTLS12})
		err = conn.HandshakeContext(ctx)
		state := conn.ConnectionState()
		_ = conn.Close()
		certs := state.PeerCertificates
		var verifyError *tls.CertificateVerificationError
		if errors.As(err, &verifyError) {
			certs = verifyError.UnverifiedCertificates
		}
		if len(certs) > 0 {
			cert := certs[0]
			result.Subject = cert.Subject.String()
			result.Issuer = cert.Issuer.String()
			result.DNSNames = cert.DNSNames
			result.NotAfter = &cert.NotAfter
		}
		if err != nil {
			if verifyError != nil {
				result.Status = "invalid"
				result.Error = fmt.Sprintf("入口证书验证失败（SNI %s）：%v", result.ServerName, verifyError.Err)
			} else {
				result.Error = "入口 TLS 握手失败或超时"
			}
			return result, err
		}
	}
	result.Status = "passed"
	result.CheckedAt = time.Now().UTC()
	return result, nil
}

func (a *app) certificateIssues(w http.ResponseWriter, r *http.Request) {
	const pageSize = 50
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 {
			apiError(w, 422, "页码必须是正整数", nil)
			return
		}
	}
	var total int
	err := a.db.QueryRow(r.Context(), `WITH active AS MATERIALIZED (
 SELECT DISTINCT md5(share_uri) AS config_hash FROM nodes WHERE enabled AND share_uri<>''
 ) SELECT count(*) FROM node_ip_pool p JOIN active n ON n.config_hash=p.config_hash
 WHERE p.certificate->>'status'='invalid'
 `).Scan(&total)
	if err != nil {
		dbError(w, err)
		return
	}
	lastPage := max(1, (total+pageSize-1)/pageSize)
	page = min(page, lastPage)
	rows, err := a.db.Query(r.Context(), `SELECT host(p.ip),p.protocol,n.id::text,n.name,p.certificate,p.checked_at
 FROM node_ip_pool p
 JOIN LATERAL (SELECT id,name FROM nodes WHERE enabled AND share_uri<>'' AND md5(share_uri)=p.config_hash ORDER BY id LIMIT 1) n ON true
 WHERE p.certificate->>'status'='invalid'
 ORDER BY p.checked_at DESC,p.ip,p.protocol LIMIT $1 OFFSET $2`, pageSize, (page-1)*pageSize)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	type issue struct {
		IP          string          `json:"ip"`
		Protocol    string          `json:"protocol"`
		NodeID      string          `json:"node_id"`
		NodeName    string          `json:"node_name"`
		Certificate json.RawMessage `json:"certificate"`
		CheckedAt   time.Time       `json:"checked_at"`
	}
	items := []issue{}
	for rows.Next() {
		var item issue
		if err = rows.Scan(&item.IP, &item.Protocol, &item.NodeID, &item.NodeName, &item.Certificate, &item.CheckedAt); err != nil {
			dbError(w, err)
			return
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	w.Header().Set("X-Page", strconv.Itoa(page))
	w.Header().Set("X-Page-Size", strconv.Itoa(pageSize))
	respond(w, 200, items)
}
