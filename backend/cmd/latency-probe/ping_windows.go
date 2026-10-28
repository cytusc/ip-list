package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	icmpDLL       = windows.NewLazySystemDLL("iphlpapi.dll")
	icmpCreate    = icmpDLL.NewProc("IcmpCreateFile")
	icmp6Create   = icmpDLL.NewProc("Icmp6CreateFile")
	icmpClose     = icmpDLL.NewProc("IcmpCloseHandle")
	icmpSendEcho  = icmpDLL.NewProc("IcmpSendEcho2Ex")
	icmp6SendEcho = icmpDLL.NewProc("Icmp6SendEcho2")
)

// Use the Windows ICMP service, as system ping does, instead of receiving echo
// replies through a raw Winsock socket (which has different firewall behavior).
// Always specify the selected physical interface's source address.
func windowsPing(ip netip.Addr, timeout time.Duration, name string) (float64, bool, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, false, fmt.Errorf("读取出口网卡 %s：%w", name, err)
	}
	source, err := netip.ParseAddr(interfaceAddress(*iface, ip.Is6()))
	if err != nil {
		family := 4
		if ip.Is6() {
			family = 6
		}
		return 0, false, fmt.Errorf("出口网卡 %s 没有 IPv%d 源地址", name, family)
	}
	return windowsICMPEcho(ip, source, timeout, name)
}

func windowsICMPEcho(ip, source netip.Addr, timeout time.Duration, name string) (float64, bool, error) {
	create, send := icmpCreate, icmpSendEcho
	if ip.Is6() {
		create, send = icmp6Create, icmp6SendEcho
	}
	for _, proc := range []*windows.LazyProc{create, send, icmpClose} {
		if err := proc.Find(); err != nil {
			return 0, false, fmt.Errorf("加载 Windows ICMP API：%w", err)
		}
	}
	handle, _, callErr := create.Call()
	if handle == ^uintptr(0) || handle == 0 {
		return 0, false, fmt.Errorf("创建 Windows ICMP 句柄：%w", callErr)
	}
	defer icmpClose.Call(handle)
	request := []byte("ip-list-physical-icmp")
	// uint64 backing storage ensures native structure alignment on amd64/arm64.
	storage := make([]uint64, 512)
	reply := unsafe.Slice((*byte)(unsafe.Pointer(&storage[0])), len(storage)*8)
	var count uintptr
	if ip.Is6() {
		src := windows.RawSockaddrInet6{Family: windows.AF_INET6, Addr: source.As16()}
		dst := windows.RawSockaddrInet6{Family: windows.AF_INET6, Addr: ip.As16()}
		// Keep conversions inside Call so Go pins/escapes every native pointer.
		count, _, callErr = send.Call(handle, 0, 0, 0,
			uintptr(unsafe.Pointer(&src)), uintptr(unsafe.Pointer(&dst)),
			uintptr(unsafe.Pointer(&request[0])), uintptr(len(request)), 0,
			uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), uintptr(max(1, timeout.Milliseconds())))
	} else {
		s, d := source.As4(), ip.As4()
		count, _, callErr = send.Call(handle, 0, 0, 0,
			uintptr(binary.LittleEndian.Uint32(s[:])), uintptr(binary.LittleEndian.Uint32(d[:])),
			uintptr(unsafe.Pointer(&request[0])), uintptr(len(request)), 0,
			uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), uintptr(max(1, timeout.Milliseconds())))
	}
	runtime.KeepAlive(request)
	runtime.KeepAlive(storage)
	if count == 0 {
		if code, ok := callErr.(syscall.Errno); ok && icmpRemoteFailure(uint32(code)) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("Windows ICMP 请求失败（网卡 %s，源地址 %s）：%w", name, source, callErr)
	}
	return parseWindowsICMPReply(reply, ip)
}
