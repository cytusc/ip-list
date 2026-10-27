package main

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
)

type generatedLink struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	Protocol string `json:"protocol"`
	URI      string `json:"uri"`
}

func nodeLink(raw, name string, ip netip.Addr) (string, error) {
	// parseShare materializes implicit Host/SNI before replacing the endpoint.
	_, u, err := parseShare(raw)
	if err != nil {
		return "", err
	}
	u.Host = net.JoinHostPort(ip.String(), u.Port())
	u.Fragment = name + " · " + ip.String()
	u.RawFragment = ""
	return u.String(), nil
}

func (a *app) generateIPLinks(w http.ResponseWriter, r *http.Request) {
	ip, err := netip.ParseAddr(r.URL.Query().Get("ip"))
	if err != nil || ip.Zone() != "" {
		apiError(w, 422, "请输入有效的 IP 地址", nil)
		return
	}
	poolRows, err := a.db.Query(r.Context(), "SELECT protocol FROM node_ip_pool WHERE ip=$1::inet AND available", ip.String())
	if err != nil {
		dbError(w, err)
		return
	}
	protocols := map[string]bool{}
	for poolRows.Next() {
		var protocol string
		if err = poolRows.Scan(&protocol); err != nil {
			poolRows.Close()
			dbError(w, err)
			return
		}
		protocols[protocol] = true
	}
	err = poolRows.Err()
	poolRows.Close()
	if err != nil {
		dbError(w, err)
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT id::text,name,share_uri FROM nodes WHERE enabled AND share_uri<>'' ORDER BY name,id`)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	links := []generatedLink{}
	for rows.Next() {
		var link generatedLink
		var raw string
		if err = rows.Scan(&link.NodeID, &link.NodeName, &raw); err != nil {
			dbError(w, err)
			return
		}
		link.Protocol = nodePoolProtocol(raw)
		if !protocols[link.Protocol] {
			continue
		}
		link.URI, err = nodeLink(raw, link.NodeName, ip)
		if err != nil {
			apiError(w, 422, "节点配置无法生成链接，请检查节点配置后重测", nil)
			return
		}
		links = append(links, link)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	if len(links) == 0 {
		apiError(w, 409, "该 IP 已无可用节点，请刷新列表或重新检测", nil)
		return
	}
	respond(w, 200, links)
}

// Keep both proxy protocol and transport as the protocol-pool boundary.
func nodePoolProtocol(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "vless" && u.Scheme != "trojan") {
		return ""
	}
	protocol := u.Query().Get("type")
	if protocol == "" {
		protocol = "tcp"
	}
	return u.Scheme + "/" + protocol
}
