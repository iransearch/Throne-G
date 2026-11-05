package autoselector

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/interrupt"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func TestRoutedSelectionStillUsesRoundRobinAndOfflineGuard(t *testing.T) {
	s := fixture()
	for _, want := range []string{"a", "b", "c", "a", "b", "c"} {
		conn, err := s.Selected(N.NetworkTCP).DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr("192.0.2.1:443"))
		if err != nil {
			t.Fatal(err)
		}
		if s.Now() != want {
			t.Fatalf("router bypassed round-robin: got %s want %s", s.Now(), want)
		}
		_ = conn.Close()
	}
	s.externalNetworkOffline = true
	if _, err := s.Selected(N.NetworkTCP).DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr("192.0.2.1:443")); err == nil {
		t.Fatal("resolved group bypassed the offline guard")
	}
	for tag, outbound := range s.outbounds {
		if outbound.(*testOutbound).attempts != 2 {
			t.Fatalf("unexpected attempts for %s", tag)
		}
	}
}

func TestRoutedAttachmentsKeepExistingInterruptPolicy(t *testing.T) {
	s := fixture()
	s.attachedGroup = interrupt.NewGroup()
	conn := &trackedConn{}
	detach := s.AttachConnection(conn)
	defer detach()
	s.interruptExternalConnections = true
	s.setSelectedLocked(s.outbounds["b"], N.NetworkTCP, "rotation", time.Now(), false)
	if conn.closed {
		t.Fatal("routine rotation interrupted an attached connection")
	}
	s.interruptExternalConnections = false
	s.setSelectedLocked(s.outbounds["c"], N.NetworkTCP, "failover", time.Now(), true)
	if conn.closed {
		t.Fatal("external interruption was not enabled")
	}
	s.interruptExternalConnections = true
	s.setSelectedLocked(s.outbounds["a"], N.NetworkTCP, "failover", time.Now(), true)
	if !conn.closed {
		t.Fatal("explicit interruption did not reach the routed connection")
	}
}
