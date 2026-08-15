//go:build !darwin && !linux

package snapshotipc

import (
	"fmt"
	"net"
)

func peerUID(_ *net.UnixConn) (int, error) {
	return 0, fmt.Errorf("snapshot peer credentials are unsupported on this platform")
}
