package main

import (
	"ThroneCore/gen"
	"ThroneCore/internal/boxdns"
	"context"
)

func (s *server) SetSystemDNS(ctx context.Context, in *gen.SetSystemDNSRequest) (*gen.EmptyResp, error) {
	_ = boxdns.Start()

	err := boxdns.DnsManagerInstance.SetSystemDNS(nil, *in.Clear)
	if err != nil {
		return nil, err
	}

	return &gen.EmptyResp{}, nil
}
