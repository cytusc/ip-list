package main

import (
	"context"
	"math"
	"strconv"
	"strings"
)

func (a *app) filterDNSSlowIPs(ctx context.Context, items []dnsCandidate) error {
	ips := make([]string, 0, len(items))
	for _, item := range items {
		if item.Pool != "cf" && item.State == "healthy" {
			ips = append(ips, item.IP)
		}
	}
	if len(ips) == 0 {
		return nil
	}
	var benchmark float64
	err := a.db.QueryRow(ctx, `SELECT COALESCE(avg(test.speed_mib_s),0)
 FROM (SELECT DISTINCT ip FROM shared_node_ip_pool WHERE available) s
 JOIN available_ip_speed_tests test ON test.ip=s.ip AND test.success`+networkInfoJoin+`
 WHERE info.asn='AS36002'`).Scan(&benchmark)
	if err != nil {
		return err
	}
	hasBenchmark := !math.IsNaN(benchmark) && !math.IsInf(benchmark, 0) && benchmark > 0
	rows, err := a.db.Query(ctx, `SELECT host(ip),speed_mib_s,success
 FROM available_ip_speed_tests WHERE ip=ANY($1::inet[])`, ips)
	if err != nil {
		return err
	}
	type speed struct {
		value   float64
		success bool
	}
	results := map[string]speed{}
	for rows.Next() {
		var ip string
		var value float64
		var success bool
		if err = rows.Scan(&ip, &value, &success); err != nil {
			rows.Close()
			return err
		}
		results[ip] = speed{value, success}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for i := range items {
		item := &items[i]
		if item.Pool == "cf" || item.State != "healthy" {
			continue
		}
		result, ok := results[item.IP]
		if !ok || !result.success || result.value <= 0 {
			item.State, item.Reason = "pending", "没有成功的下载测速结果，暂不进入节点 DNS"
			continue
		}
		minimum := 9.0
		switch strings.ToUpper(item.Country) {
		case "HK":
			if !hasBenchmark {
				item.State, item.Reason = "pending", "AS36002 没有成功测速基准，暂不进入节点 DNS"
				continue
			}
			minimum = benchmark
		case "SG":
			minimum = 7
		}
		if result.value < minimum {
			item.State = "pending"
			if item.Country == "HK" {
				item.Reason = "下载速度低于 AS36002 基准 " + strconv.FormatFloat(minimum, 'f', 2, 64) + " MiB/s"
			} else {
				item.Reason = "下载速度低于节点 DNS 门槛 " + strconv.FormatFloat(minimum, 'f', 2, 64) + " MiB/s"
			}
		}
	}
	return nil
}
