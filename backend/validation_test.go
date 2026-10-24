package main

import "testing"

func TestNetworkCIDR(t *testing.T) {
	cases := []struct {
		cidr  string
		valid bool
	}{
		{"192.168.1.0/24", true}, {"192.168.1.1/24", false},
		{"0.0.0.0/0", true}, {"192.168.1.1/32", true},
		{"192.168.1.0/33", false}, {"999.1.0.0/16", false},
		{"2001:db8::/32", true}, {"2001:db8::1/32", false},
		{"::/0", true}, {"::1/128", true}, {"::1/129", false},
		{"192.168.1.1", false}, {"fe80::%en0/64", false},
	}
	for _, tc := range cases {
		t.Run(tc.cidr, func(t *testing.T) {
			in := ConfigInput{Name: "test", CIDR: tc.cidr}
			fields := in.validate("networks")
			if (fields["cidr"] == "") != tc.valid {
				t.Errorf("CIDR %s: fields=%v", tc.cidr, fields)
			}
		})
	}
}

func TestNodeValidation(t *testing.T) {
	for _, address := range []string{"192.168.1.1", "2001:db8::1", "node.example.com", "localhost"} {
		in := ConfigInput{Name: "节点", Address: address, Port: 8080}
		if fields := in.validate("nodes"); len(fields) != 0 {
			t.Errorf("valid address %s: %v", address, fields)
		}
	}
	for _, address := range []string{"", "https://example.com", "127.0.0.1:8080", "999.999.1.1", "node space"} {
		in := ConfigInput{Name: "节点", Address: address, Port: 8080}
		if in.validate("nodes")["address"] == "" {
			t.Errorf("accepted invalid address %s", address)
		}
	}
	in := ConfigInput{Name: " ", Address: "127.0.0.1", Port: 65536}
	fields := in.validate("nodes")
	if fields["name"] == "" || fields["port"] == "" {
		t.Fatal("missing name/port validation")
	}
}
