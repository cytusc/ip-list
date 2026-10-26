package main

import (
	"golang.org/x/sys/unix"
	"net"
	"syscall"
)

func bindSocket(raw syscall.RawConn, iface net.Interface, ipv6 bool) error {
	var bindErr error
	err := raw.Control(func(fd uintptr) {
		level, option := unix.IPPROTO_IP, unix.IP_BOUND_IF
		if ipv6 {
			level, option = unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF
		}
		bindErr = unix.SetsockoptInt(int(fd), level, option, iface.Index)
	})
	if err != nil {
		return err
	}
	return bindErr
}
