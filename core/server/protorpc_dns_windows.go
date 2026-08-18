//go:build windows

package main

import (
	"ThroneCore/gen"
	"context"
)

func setSystemDNSProtoRPC(in *gen.SetSystemDNSRequest, out *gen.EmptyResp) error {
	return adaptProtoRPC(out, func() (*gen.EmptyResp, error) {
		return globalServer.SetSystemDNS(context.Background(), in)
	})
}
