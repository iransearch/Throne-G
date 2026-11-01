package autoselector

import (
	"context"
	"net"
	"testing"

	"ThroneCore/internal/hostkeydiag"
	"ThroneCore/internal/netdiag"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type hostkeyTestRouter struct {
	adapter.Router
	selector *AutoSelector
}

func (*hostkeyTestRouter) AppendTracker(adapter.ConnectionTracker) {}
func (r *hostkeyTestRouter) RouteConnectionEx(ctx context.Context, _ net.Conn, m adapter.InboundContext, close N.CloseHandlerFunc) {
	ctx = adapter.WithContext(ctx, &m)
	c, err := r.selector.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr("104.26.12.47:443"))
	if c != nil {
		_ = c.Close()
	}
	close(err)
}

func TestHostkeyDiagnosticsKeepExistingPoolRetriesAndCallback(t *testing.T) {
	s := fixture()
	s.outbounds["a"].(*testOutbound).fail = true
	ctx := hostkeydiag.WithObserver(netdiag.WithBox(context.Background()))
	ctx = log.ContextWithID(ctx, log.ID{ID: 12345})
	r := hostkeydiag.ObserveRouter(&hostkeyTestRouter{selector: s})
	callback := 0
	r.RouteConnectionEx(ctx, nil, adapter.InboundContext{InboundType: "mixed", Network: "tcp", Destination: M.Socksaddr{Fqdn: "invapi.hostkey.com", Port: 443}}, func(err error) {
		callback++
		if err != nil {
			t.Fatal(err)
		}
	})
	if callback != 1 || s.outbounds["a"].(*testOutbound).attempts != 1 || s.outbounds["b"].(*testOutbound).attempts != 1 || s.outbounds["c"].(*testOutbound).attempts != 0 {
		t.Fatal("diagnostics changed existing retry selection or callback")
	}
}
