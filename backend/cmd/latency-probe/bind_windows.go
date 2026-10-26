package main

import (
	"encoding/binary"
	"golang.org/x/sys/windows"
	"net"
	"syscall"
)

func bindSocket(raw syscall.RawConn, iface net.Interface, ipv6 bool) error {
	var bindErr error
	err := raw.Control(func(fd uintptr) {
		level, index := windows.IPPROTO_IP, iface.Index
		// IP_UNICAST_IF takes a network-byte-order index, IPV6_UNICAST_IF host order.
		if ipv6 {
			level = windows.IPPROTO_IPV6
		} else {
			var b [4]byte
			binary.BigEndian.PutUint32(b[:], uint32(index))
			index = int(binary.NativeEndian.Uint32(b[:]))
		}
		bindErr = windows.SetsockoptInt(windows.Handle(fd), level, 31, index)
	})
	if err != nil {
		return err
	}
	return bindErr
}
