package main

import "testing"

func TestDNSSpeedPassed(t *testing.T) {
	for _, tc := range []struct {
		country string
		speed   float64
		want    bool
	}{
		{"HK", 9.99, false},
		{"HK", 10, false},
		{"hk", 10.01, true},
		{"SG", 6.99, false},
		{"SG", 7, true},
		{"US", 8.99, false},
		{"US", 9, true},
	} {
		if got := dnsSpeedPassed(tc.country, tc.speed); got != tc.want {
			t.Errorf("dnsSpeedPassed(%q, %v) = %v, want %v", tc.country, tc.speed, got, tc.want)
		}
	}
}
