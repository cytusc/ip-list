package ipguard

import (
	"context"
	"strings"
)

func dnsSpeedPassed(country string, speed float64) bool {
	switch strings.ToUpper(country) {
	case "HK":
		return speed > 10
	case "SG":
		return speed >= 7
	default:
		return speed >= 9
	}
}

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
		if !dnsSpeedPassed(item.Country, result.value) {
			item.State = "pending"
			item.Reason = "下载速度未达到节点 DNS 门槛：香港需 >10 MiB/s，新加坡需 ≥7 MiB/s，其他地区需 ≥9 MiB/s"
		}
	}
	return nil
}

func (a *app) availableIPSpeedStatus(ctx context.Context, items []availableIP) error {
	ips := make([]string, len(items))
	for i := range items {
		ips[i] = items[i].IP
	}
	if len(ips) == 0 {
		return nil
	}
	rows, err := a.db.Query(ctx, availableIPCandidatesSQL+` SELECT host(ip),country FROM candidates WHERE ip=ANY($5::inet[])`, "", "", "", "", ips)
	if err != nil {
		return err
	}
	countries := map[string]string{}
	for rows.Next() {
		var ip, country string
		if err = rows.Scan(&ip, &country); err != nil {
			rows.Close()
			return err
		}
		countries[ip] = country
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for i := range items {
		result := items[i].SpeedTest
		items[i].SpeedPassed = result != nil && result.Success && dnsSpeedPassed(countries[items[i].IP], result.SpeedMiBs)
	}
	return nil
}
