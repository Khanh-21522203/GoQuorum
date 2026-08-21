//go:build linux
// +build linux

package iouring

import (
	"errors"
	"syscall"
	"unsafe"
)

var zero uintptr

func bytes2iovec(bs [][]byte) []syscall.Iovec {
	iovecs := make([]syscall.Iovec, len(bs))
	for i, b := range bs {
		iovecs[i].SetLen(len(b))
		if len(b) > 0 {
			iovecs[i].Base = &b[0]
		} else {
			iovecs[i].Base = (*byte)(unsafe.Pointer(&zero))
		}
	}
	return iovecs
}

// htons converts a 16-bit port number to network byte order. Swapping the
// two bytes produces the correct on-the-wire representation regardless of
// the host's own endianness.
func htons(port uint16) uint16 {
	return (port << 8) | (port >> 8)
}

// sockaddr and anyToSockaddr replace this package's original
// //go:linkname pulls into the unexported syscall.Sockaddr.sockaddr method
// and syscall.anyToSockaddr function. Go's linker rejects unauthorized
// linkname references into standard-library internals starting with the
// toolchain this module is built against, so these are reimplemented here
// against only exported syscall types, covering the address families this
// module's Connect/Sendmsg/Recvmsg support (AF_INET, AF_INET6, AF_UNIX).
func sockaddr(addr syscall.Sockaddr) (unsafe.Pointer, uint32, error) {
	switch sa := addr.(type) {
	case *syscall.SockaddrInet4:
		raw := syscall.RawSockaddrInet4{
			Family: syscall.AF_INET,
			Port:   htons(uint16(sa.Port)),
			Addr:   sa.Addr,
		}
		return unsafe.Pointer(&raw), uint32(unsafe.Sizeof(raw)), nil

	case *syscall.SockaddrInet6:
		raw := syscall.RawSockaddrInet6{
			Family:   syscall.AF_INET6,
			Port:     htons(uint16(sa.Port)),
			Scope_id: sa.ZoneId,
			Addr:     sa.Addr,
		}
		return unsafe.Pointer(&raw), uint32(unsafe.Sizeof(raw)), nil

	case *syscall.SockaddrUnix:
		var raw syscall.RawSockaddrUnix
		raw.Family = syscall.AF_UNIX
		name := sa.Name
		if len(name) >= len(raw.Path) {
			return nil, 0, errors.New("iouring: unix socket path too long")
		}
		for i := 0; i < len(name); i++ {
			raw.Path[i] = int8(name[i])
		}
		length := uint32(unsafe.Offsetof(raw.Path)) + uint32(len(name)) + 1
		return unsafe.Pointer(&raw), length, nil

	default:
		return nil, 0, errors.New("iouring: unsupported sockaddr type")
	}
}

func anyToSockaddr(rsa *syscall.RawSockaddrAny) (syscall.Sockaddr, error) {
	switch rsa.Addr.Family {
	case syscall.AF_INET:
		raw := (*syscall.RawSockaddrInet4)(unsafe.Pointer(rsa))
		return &syscall.SockaddrInet4{
			Port: int(htons(raw.Port)),
			Addr: raw.Addr,
		}, nil

	case syscall.AF_INET6:
		raw := (*syscall.RawSockaddrInet6)(unsafe.Pointer(rsa))
		return &syscall.SockaddrInet6{
			Port:   int(htons(raw.Port)),
			ZoneId: raw.Scope_id,
			Addr:   raw.Addr,
		}, nil

	case syscall.AF_UNIX:
		raw := (*syscall.RawSockaddrUnix)(unsafe.Pointer(rsa))
		n := 0
		for n < len(raw.Path) && raw.Path[n] != 0 {
			n++
		}
		buf := make([]byte, n)
		for i := 0; i < n; i++ {
			buf[i] = byte(raw.Path[i])
		}
		return &syscall.SockaddrUnix{Name: string(buf)}, nil

	default:
		return nil, errors.New("iouring: unsupported address family")
	}
}
