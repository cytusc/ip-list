package main

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// IP_STATUS values documenting an actual timeout or a remote ICMP error.
// Local resource, configuration and API errors must never invalidate an IP.
func icmpRemoteFailure(status uint32) bool {
	switch status {
	case 11002, 11003, 11004, 11005, 11009, 11010, 11013, 11014,
		11040, 11041:
		return true
	}
	return false
}

func parseWindowsICMPReply(reply []byte, target netip.Addr) (float64, bool, error) {
	// ICMP_ECHO_REPLY begins with address, status, RTT. ICMPV6_ECHO_REPLY
	// begins with IPV6_ADDRESS_EX (28 bytes), followed by status and RTT.
	offset := 4
	if target.Is6() {
		offset = 28
	}
	if len(reply) < offset+8 {
		return 0, false, fmt.Errorf("Windows ICMP 返回数据过短")
	}
	status := binary.LittleEndian.Uint32(reply[offset:])
	if status != 0 {
		if icmpRemoteFailure(status) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("Windows ICMP 本机检测错误，状态码 %d", status)
	}
	var peer netip.Addr
	if target.Is6() {
		peer = netip.AddrFrom16([16]byte(reply[8:24]))
	} else {
		peer = netip.AddrFrom4([4]byte(reply[:4]))
	}
	if peer != target {
		return 0, false, fmt.Errorf("Windows ICMP 回复地址不匹配：%s", peer)
	}
	return max(0.01, float64(binary.LittleEndian.Uint32(reply[offset+4:]))), true, nil
}
