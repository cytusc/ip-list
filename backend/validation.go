package main

import (
	"github.com/go-playground/validator/v10"
	"net/netip"
	"strconv"
	"strings"
)

var validate = validator.New()

type ConfigInput struct {
	ASN         string `json:"asn"`
	Provider    string `json:"provider"`
	ShareURI    string `json:"share_uri"`
	Name        string `json:"name"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description"`
	CIDR        string `json:"cidr"`
}

func (in *ConfigInput) validate(kind string) map[string]string {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	fields := map[string]string{}
	if validate.Var(in.Name, "required,max=80") != nil {
		fields["name"] = "请输入 1–80 个字符的名称"
	}
	if validate.Var(in.Description, "max=500") != nil {
		fields["description"] = "备注最多 500 个字符"
	}
	if kind == "nodes" {
		in.ShareURI = strings.TrimSpace(in.ShareURI)
		if in.ShareURI != "" {
			_, u, err := parseShare(in.ShareURI)
			if err != nil {
				fields["share_uri"] = err.Error()
			} else {
				in.Address = u.Hostname()
				in.Port, _ = strconv.Atoi(u.Port())
			}
		}
		in.Address = strings.ToLower(strings.TrimSpace(in.Address))
		_, ipErr := netip.ParseAddr(in.Address)
		if validate.Var(in.Address, "required,max=253,ip|hostname_rfc1123") != nil || (ipErr != nil && strings.Trim(in.Address, "0123456789.") == "") {
			fields["address"] = "请输入有效的 IP 地址或域名，不含协议和端口"
		}
		if in.Port < 1 || in.Port > 65535 {
			fields["port"] = "端口范围为 1–65535"
		}
	} else {
		in.ASN = strings.ToUpper(strings.TrimSpace(in.ASN))
		in.Provider = strings.TrimSpace(in.Provider)
		if in.ASN != "" {
			value, err := normalizeASNs(in.ASN)
			if err != nil {
				fields["asn"] = "请输入有效 ASN，多个用逗号分隔，例如 AS906, AS32519"
			} else {
				in.ASN = value
			}
		}
		if validate.Var(in.Provider, "max=100") != nil {
			fields["provider"] = "服务商名称最多 100 个字符"
		}
		in.CIDR = strings.TrimSpace(in.CIDR)
		prefix, err := netip.ParsePrefix(in.CIDR)
		if err != nil || prefix != prefix.Masked() || strings.Contains(in.CIDR, "%") {
			fields["cidr"] = "请输入网络地址，如 192.168.1.0/24 或 2001:db8::/32，主机位需为 0"
		} else {
			in.CIDR = prefix.String()
		}

	}
	return fields
}
