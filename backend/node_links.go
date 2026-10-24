package main

import (
	"net"
	"net/http"
	"net/netip"
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
	rows, err := a.db.Query(r.Context(), `SELECT n.id::text,n.name,s.protocol,n.share_uri
 FROM ip_results s JOIN nodes n ON n.id=s.node_id
 WHERE s.ip=$1::inet AND s.available AND n.enabled AND s.config_hash=md5(n.share_uri)
 ORDER BY s.protocol,n.name,n.id`, ip.String())
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	links := []generatedLink{}
	for rows.Next() {
		var link generatedLink
		var raw string
		if err = rows.Scan(&link.NodeID, &link.NodeName, &link.Protocol, &raw); err != nil {
			dbError(w, err)
			return
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
