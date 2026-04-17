//go:build !windows

package punch

import (
	"net"
	"syscall"
)

// enableBroadcast sets SO_BROADCAST on conn so we can send to 255.255.255.255.
func enableBroadcast(conn *net.UDPConn) {
	rc, err := conn.SyscallConn()
	if err != nil {
		return
	}
	rc.Control(func(fd uintptr) { //nolint:errcheck
		syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1) //nolint:errcheck
	})
}
