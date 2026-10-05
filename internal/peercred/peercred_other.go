//go:build !linux && !darwin

package peercred

import (
	"fmt"
	"net"
)

func PeerPID(net.Conn) (int, error) {
	return 0, fmt.Errorf("attached sessions require Linux or macOS peer credentials")
}
func Descendant(int, int) bool { return false }
