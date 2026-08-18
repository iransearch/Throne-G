//go:build !windows

package main

import "ThroneCore/gen"

// SetSystemDNS is a no-op on platforms where Throne does not expose the
// Windows DNS setter, matching the pre-existing ProtoRPC service behavior.
func setSystemDNSProtoRPC(_ *gen.SetSystemDNSRequest, _ *gen.EmptyResp) error {
	return nil
}
