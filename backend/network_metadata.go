package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type networkMetadata struct {
	ASN      string `json:"asn"`
	Provider string `json:"provider"`
}

func prefixLastAddress(prefix netip.Prefix) netip.Addr {
	addr := prefix.Masked().Addr()
	if addr.Is4() {
		bytes := addr.As4()
		for bit := prefix.Bits(); bit < 32; bit++ {
			bytes[bit/8] |= 1 << (7 - bit%8)
		}
		return netip.AddrFrom4(bytes)
	}
	bytes := addr.As16()
	for bit := prefix.Bits(); bit < 128; bit++ {
		bytes[bit/8] |= 1 << (7 - bit%8)
	}
	return netip.AddrFrom16(bytes)
}

func publicMetadataPrefix(p netip.Prefix) bool {
	ip := p.Addr()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Is4In6() {
		return false
	}
	for _, reserved := range []string{"100.64.0.0/10", "192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "198.18.0.0/15", "2001:db8::/32"} {
		if netip.MustParsePrefix(reserved).Contains(ip) {
			return false
		}
	}
	return true
}

// RIPEstat supplies the originating ASN and its registered holder, not a
// guessed retail brand. Preserve all origins; refuse partially covered prefixes.
func lookupNetworkMetadata(ctx context.Context, client *http.Client, base string, prefix netip.Prefix) (networkMetadata, error) {
	var result networkMetadata
	get := func(endpoint, resource string, target any) error {
		req, err := http.NewRequestWithContext(ctx, "GET", base+"/data/"+endpoint+"/data.json?resource="+url.QueryEscape(resource), nil)
		if err != nil {
			return err
		}
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("RIPE HTTP %d", response.StatusCode)
		}
		return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target)
	}
	last := prefixLastAddress(prefix)
	cursor := prefix.Masked().Addr()
	rawASNs := []string{}
	for routes := 0; prefix.Contains(cursor); routes++ {
		if routes >= 64 {
			return result, errors.New("too many announced routes covering this range")
		}
		var network struct {
			Status string
			Data   struct {
				ASNs   []string `json:"asns"`
				Prefix string
			}
		}
		if err := get("network-info", cursor.String(), &network); err != nil {
			return result, err
		}
		announced, err := netip.ParsePrefix(network.Data.Prefix)
		if network.Status != "ok" || err != nil || len(network.Data.ASNs) == 0 || !announced.Contains(cursor) {
			return result, errors.New("no unique ASN covering this range")
		}
		rawASNs = append(rawASNs, network.Data.ASNs...)
		routeLast := prefixLastAddress(announced)
		if routeLast.Compare(last) >= 0 {
			break
		}
		cursor = routeLast.Next()
	}
	asns, err := normalizeASNs(strings.Join(rawASNs, ","))
	if err != nil {
		return result, err
	}
	holders := []string{}
	for _, asn := range strings.Split(asns, ", ") {
		var overview struct {
			Status string
			Data   struct{ Holder string }
		}
		if err := get("as-overview", asn, &overview); err != nil {
			return result, err
		}
		holder := strings.TrimSpace(overview.Data.Holder)
		if overview.Status != "ok" || holder == "" {
			return result, errors.New("missing ASN holder")
		}
		if !strings.Contains(strings.Join(holders, " / "), holder) {
			holders = append(holders, holder)
		}
	}
	holder := strings.Join(holders, " / ")
	// Keep the existing schema's 100-character provider limit.
	result = networkMetadata{ASN: asns, Provider: string([]rune(holder)[:min(100, len([]rune(holder)))])}
	return result, nil
}

func (a *app) networkMetadata(ctx context.Context, prefix netip.Prefix) (networkMetadata, error) {
	key := "ipfilter:network-metadata:v1:" + prefix.String()
	if a.redis != nil {
		cacheCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		raw, err := a.redis.Get(cacheCtx, key).Bytes()
		cancel()
		var cached networkMetadata
		if err == nil && json.Unmarshal(raw, &cached) == nil && cached.ASN != "" && cached.Provider != "" {
			return cached, nil
		}
	}
	// Leave time in the API request budget for the actual network write after
	// this optional external lookup. Missing metadata is retried in the background.
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := lookupNetworkMetadata(lookupCtx, http.DefaultClient, "https://stat.ripe.net", prefix)
	if err == nil && a.redis != nil {
		raw, _ := json.Marshal(result)
		cacheCtx, stop := context.WithTimeout(ctx, 150*time.Millisecond)
		_ = a.redis.Set(cacheCtx, key, raw, 24*time.Hour).Err()
		stop()
	}
	return result, err
}

func normalizeASNs(raw string) (string, error) {
	seen := map[uint64]bool{}
	values := []uint64{}
	for _, part := range strings.Split(strings.ToUpper(raw), ",") {
		number := strings.TrimPrefix(strings.TrimSpace(part), "AS")
		value, err := strconv.ParseUint(number, 10, 32)
		if err != nil || value == 0 || strings.Trim(number, "0123456789") != "" {
			return "", errors.New("invalid ASN")
		}
		if !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = "AS" + strconv.FormatUint(value, 10)
	}
	result := strings.Join(parts, ", ")
	if len(result) > 255 {
		return "", errors.New("too many ASNs")
	}
	return result, nil
}

// Retry missing metadata independently of page requests and scan jobs.
func (a *app) maintainNetworkMetadata(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		a.fillMissingNetworkMetadata(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *app) fillMissingNetworkMetadata(ctx context.Context) {
	rows, err := a.db.Query(ctx, "SELECT id::text,cidr::text,asn,provider FROM networks WHERE asn='' OR provider='' ORDER BY updated_at,id")
	if err != nil {
		log.Printf("网段归属补全读取失败: %v", err)
		return
	}
	type missing struct{ id, cidr, asn, provider string }
	items := []missing{}
	for rows.Next() {
		var item missing
		if err = rows.Scan(&item.id, &item.cidr, &item.asn, &item.provider); err != nil {
			break
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		log.Printf("网段归属补全读取失败: %v", err)
		return
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return
		}
		prefix, err := netip.ParsePrefix(item.cidr)
		if err != nil || !publicMetadataPrefix(prefix) {
			continue
		}
		metadata, err := a.networkMetadata(ctx, prefix)
		if err != nil {
			log.Printf("网段归属补全暂未成功 cidr=%s error=%v", item.cidr, err)
			continue
		}
		if item.asn != "" && item.asn != metadata.ASN {
			continue
		}
		// Compare the old values so concurrent manual edits/CIDR changes win.
		_, err = a.db.Exec(ctx, `UPDATE networks SET
		 asn=CASE WHEN asn='' THEN $5 ELSE asn END,
		 provider=CASE WHEN provider='' THEN $6 ELSE provider END,updated_at=now()
		 WHERE id=$1 AND cidr=$2::cidr AND asn=$3 AND provider=$4`, item.id, item.cidr, item.asn, item.provider, metadata.ASN, metadata.Provider)
		if err != nil {
			log.Printf("网段归属补全保存失败 cidr=%s error=%v", item.cidr, err)
		}
	}
}
