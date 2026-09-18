package xray

import (
	"net"

	hyobfs "github.com/apernet/hysteria/extras/v2/obfs"
)

// Xray's connected PacketConn adapter intentionally exposes only net.PacketConn.
// Gecko v2.12.3 nevertheless always exposes SyscallConn, returning ErrUnsupported
// for this adapter. quic-go treats that error as fatal before sending a handshake.
// Preserve Gecko's packet processing while exposing only the supported interface.
type xrayGeckoPacketConn struct {
	net.PacketConn
}

func wrapXrayGecko(conn net.PacketConn, options hyobfs.GeckoOptions) (net.PacketConn, error) {
	wrapped, err := hyobfs.WrapPacketConnGecko(conn, options)
	if err != nil {
		return nil, err
	}
	return &xrayGeckoPacketConn{PacketConn: wrapped}, nil
}
