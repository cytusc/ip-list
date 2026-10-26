package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"log"
	"net"
	"net/netip"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func windowsPing(ip netip.Addr, timeout time.Duration, name string) (float64, bool) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, false
	}
	source := interfaceAddress(*iface, ip.Is6())
	if source == "" {
		return 0, false
	}
	network, protocol := "ip4:icmp", 1
	var request, reply icmp.Type = ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
	if ip.Is6() {
		network, protocol = "ip6:ipv6-icmp", 58
		request, reply = ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
	}
	conn, err := net.ListenIP(network, &net.IPAddr{IP: net.ParseIP(source)})
	if err != nil {
		log.Printf("ICMP 原始套接字失败，请检查管理员权限：%v", err)
		return 0, false
	}
	defer conn.Close()
	raw, err := conn.SyscallConn()
	if err != nil || bindSocket(raw, *iface, ip.Is6()) != nil {
		return 0, false
	}
	payload := make([]byte, 24)
	if _, err = rand.Read(payload); err != nil {
		return 0, false
	}
	id := int(binary.BigEndian.Uint16(payload[:2]))
	seq := int(binary.BigEndian.Uint16(payload[2:4]))
	packet, err := (&icmp.Message{Type: request, Body: &icmp.Echo{ID: id, Seq: seq, Data: payload}}).Marshal(nil)
	if err != nil {
		return 0, false
	}
	conn.SetDeadline(time.Now().Add(timeout))
	start := time.Now()
	if _, err = conn.WriteToIP(packet, &net.IPAddr{IP: net.IP(ip.AsSlice())}); err != nil {
		return 0, false
	}
	buffer := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFromIP(buffer)
		if err != nil {
			return 0, false
		}
		elapsed := time.Since(start)
		if !peer.IP.Equal(net.IP(ip.AsSlice())) {
			continue
		}
		message, err := icmp.ParseMessage(protocol, buffer[:n])
		if err != nil || message.Type != reply {
			continue
		}
		echo, ok := message.Body.(*icmp.Echo)
		if ok && echo.ID == id && echo.Seq == seq && bytes.Equal(echo.Data, payload) {
			return max(0.01, float64(elapsed)/float64(time.Millisecond)), true
		}
	}
}
