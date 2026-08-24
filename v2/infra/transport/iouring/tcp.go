package iouring

import (
	"fmt"
	"net"
	"strconv"
	"syscall"
)

// listenBacklog is the backlog passed to syscall.Listen.
const listenBacklog = 128

// connectTCP resolves addr and opens a blocking TCP socket connection.
func connectTCP(addr string) (int, error) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return -1, fmt.Errorf("iouring: resolving %q: %w", addr, err)
	}
	family, sa, err := sockaddrFor(tcpAddr)
	if err != nil {
		return -1, err
	}

	fd, err := syscall.Socket(family, syscall.SOCK_STREAM, 0)
	if err != nil {
		return -1, fmt.Errorf("iouring: socket: %w", err)
	}
	if err := syscall.Connect(fd, sa); err != nil {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("iouring: connect to %q: %w", addr, err)
	}
	return fd, nil
}

// listenTCP resolves addr, binds, and listens on a TCP socket.
func listenTCP(addr string) (int, error) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return -1, fmt.Errorf("iouring: resolving %q: %w", addr, err)
	}
	family, sa, err := sockaddrFor(tcpAddr)
	if err != nil {
		return -1, err
	}

	fd, err := syscall.Socket(family, syscall.SOCK_STREAM, 0)
	if err != nil {
		return -1, fmt.Errorf("iouring: socket: %w", err)
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("iouring: setsockopt SO_REUSEADDR: %w", err)
	}
	if err := syscall.Bind(fd, sa); err != nil {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("iouring: bind %q: %w", addr, err)
	}
	if err := syscall.Listen(fd, listenBacklog); err != nil {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("iouring: listen: %w", err)
	}
	return fd, nil
}

// sockaddrFor converts a *net.TCPAddr into a syscall.Sockaddr.
func sockaddrFor(addr *net.TCPAddr) (family int, sa syscall.Sockaddr, err error) {
	if ip4 := addr.IP.To4(); ip4 != nil {
		sa4 := &syscall.SockaddrInet4{Port: addr.Port}
		copy(sa4.Addr[:], ip4)
		return syscall.AF_INET, sa4, nil
	}
	ip16 := addr.IP.To16()
	if ip16 == nil {
		return 0, nil, fmt.Errorf("iouring: %q did not resolve to a usable IP", addr)
	}
	sa6 := &syscall.SockaddrInet6{Port: addr.Port}
	copy(sa6.Addr[:], ip16)
	return syscall.AF_INET6, sa6, nil
}

// sockaddrToString renders a syscall.Sockaddr as a "host:port" string.
func sockaddrToString(sa syscall.Sockaddr) (string, error) {
	switch v := sa.(type) {
	case *syscall.SockaddrInet4:
		return net.JoinHostPort(net.IP(v.Addr[:]).String(), strconv.Itoa(v.Port)), nil
	case *syscall.SockaddrInet6:
		return net.JoinHostPort(net.IP(v.Addr[:]).String(), strconv.Itoa(v.Port)), nil
	default:
		return "", fmt.Errorf("iouring: unsupported sockaddr type %T", sa)
	}
}
