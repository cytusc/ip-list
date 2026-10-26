package main

import (
	"golang.org/x/sys/unix"
	"net"
	"syscall"
)

func bindSocket(raw syscall.RawConn, iface net.Interface, _ bool) error {
	var bindErr error
	err := raw.Control(func(fd uintptr) {
		bindErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface.Name)
	})
	if err != nil {
		return err
	}
	return bindErr
}
