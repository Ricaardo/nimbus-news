//go:build darwin

package snapshotipc

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func peerUID(connection *net.UnixConn) (int, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid int
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			controlErr = err
			return
		}
		uid = int(credentials.Uid)
	}); err != nil {
		return 0, err
	}
	if controlErr != nil {
		return 0, fmt.Errorf("snapshot peer credentials: %w", controlErr)
	}
	return uid, nil
}
