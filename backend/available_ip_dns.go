package main

import (
	"context"
	"encoding/json"
)

func (a *app) availableIPDNSSyncStatus(ctx context.Context, items []availableIP) error {
	var configRaw, reportRaw []byte
	err := a.db.QueryRow(ctx, `SELECT config,COALESCE((
 SELECT report FROM dns_sync_runs WHERE report->>'mode'='sync' ORDER BY id DESC LIMIT 1
),'{}'::jsonb) FROM dns_settings WHERE id`).Scan(&configRaw, &reportRaw)
	if err != nil {
		return err
	}
	var config dnsConfig
	if err = json.Unmarshal(configRaw, &config); err != nil {
		return err
	}
	for i := range items {
		items[i].NodeDNS, items[i].URLDNS = "unknown", "unknown"
		if !config.Enabled {
			items[i].NodeDNS = "disabled"
		}
		if !config.WebsiteEnabled {
			items[i].URLDNS = "disabled"
		}
	}
	var report dnsReport
	if err = json.Unmarshal(reportRaw, &report); err != nil {
		return err
	}
	if report.Mode != "sync" || report.Domain != config.Domain {
		return nil
	}
	for i := range items {
		items[i].NodeDNS = "not_synced"
		if report.Error != "" {
			items[i].NodeDNS = "unknown"
		}
		items[i].URLDNS = "not_synced"
		if report.Error != "" {
			items[i].URLDNS = "unknown"
		}
	}
	for _, record := range report.Records {
		urlRecord := dnsRecordKey(record.Name, record.Type) == dnsRecordKey("url."+config.Domain, record.Type)
		values := record.Current
		if record.Applied {
			values = record.Desired
		}
		for _, ip := range values {
			for i := range items {
				if items[i].IP != ip {
					continue
				}
				if urlRecord {
					items[i].URLDNS = "synced"
				} else {
					items[i].NodeDNS = "synced"
				}
			}
		}
	}
	return nil
}
