package main

import (
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/trafficcontrol"
)

func TestConnectionOwnerPreservesSinglePathRPC(t *testing.T) {
	path := filepath.Join("apps", "browser.exe")
	for _, owner := range []*adapter.ConnectionOwner{nil, {}, {ProcessPaths: []string{path, "second-path"}}} {
		metadata := &trafficcontrol.TrackerMetadata{Metadata: adapter.InboundContext{ProcessInfo: owner}, Upload: new(atomic.Int64), Download: new(atomic.Int64)}
		got := connMetaToProto(metadata)
		wantPath, wantName := "", ""
		if owner != nil && len(owner.ProcessPaths) > 0 {
			wantPath, wantName = path, "browser.exe"
		}
		if got.GetProcessPath() != wantPath || got.GetProcess() != wantName {
			t.Fatalf("connection metadata changed: got %q/%q want %q/%q", got.GetProcessPath(), got.GetProcess(), wantPath, wantName)
		}
	}
}
