package xray

import (
	"ThroneCore/gen"
	"context"
	"encoding/json"
	"fmt"
	stdnet "net"
	"strings"
	"sync"
	"time"

	sbtls "github.com/sagernet/sing-box/common/tls"
	sblog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-quic/hysteria"
	hy2 "github.com/sagernet/sing-quic/hysteria2"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/xtls/xray-core/common"
	xbuf "github.com/xtls/xray-core/common/buf"
	xerrors "github.com/xtls/xray-core/common/errors"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/task"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
)

const xrayHysteria2Protocol = "throne-hysteria2"

func init() {
	common.Must(common.RegisterConfig((*gen.XrayHysteria2Config)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return newXrayHysteria2Outbound(ctx, config.(*gen.XrayHysteria2Config))
	}))
}

type xrayHysteria2Outbound struct {
	config *gen.XrayHysteria2Config

	clientMu sync.Mutex
	client   *hy2.Client
}

var _ proxy.Outbound = (*xrayHysteria2Outbound)(nil)

func newXrayHysteria2Outbound(_ context.Context, config *gen.XrayHysteria2Config) (*xrayHysteria2Outbound, error) {
	if err := validateXrayHysteria2Proto(config); err != nil {
		return nil, err
	}
	return &xrayHysteria2Outbound{config: config}, nil
}

func (h *xrayHysteria2Outbound) Close() error {
	h.clientMu.Lock()
	client := h.client
	h.client = nil
	h.clientMu.Unlock()
	if client != nil {
		return client.CloseWithError(stdnet.ErrClosed)
	}
	return nil
}

func (h *xrayHysteria2Outbound) getClient(ctx context.Context, dialer internet.Dialer) (*hy2.Client, error) {
	h.clientMu.Lock()
	defer h.clientMu.Unlock()
	if h.client != nil {
		return h.client, nil
	}

	var tlsOptions option.OutboundTLSOptions
	if err := json.Unmarshal([]byte(h.config.GetTlsJson()), &tlsOptions); err != nil {
		return nil, fmt.Errorf("hysteria2 xray: invalid TLS options: %w", err)
	}
	if !tlsOptions.Enabled {
		return nil, fmt.Errorf("hysteria2 xray: TLS is required")
	}
	logger := sblog.NewNOPFactory().Logger()
	lifetimeCtx := context.WithoutCancel(ctx)
	tlsConfig, err := sbtls.NewClient(lifetimeCtx, logger, h.config.GetServer(), tlsOptions)
	if err != nil {
		return nil, fmt.Errorf("hysteria2 xray: create TLS client: %w", err)
	}

	var hopInterval time.Duration
	if value := strings.TrimSpace(h.config.GetHopInterval()); value != "" {
		hopInterval, err = time.ParseDuration(value)
		if err != nil {
			return nil, fmt.Errorf("hysteria2 xray: invalid hop interval %q: %w", value, err)
		}
	}

	clientOptions := hy2.ClientOptions{
		Context:            lifetimeCtx,
		Dialer:             &xrayHysteriaServerDialer{dialer: dialer},
		Logger:             logger,
		ServerAddress:      M.ParseSocksaddrHostPort(h.config.GetServer(), uint16(h.config.GetServerPort())),
		ServerPorts:        append([]string(nil), h.config.GetServerPorts()...),
		HopInterval:        hopInterval,
		SendBPS:            uint64(h.config.GetUpMbps()) * hysteria.MbpsToBps,
		ReceiveBPS:         uint64(h.config.GetDownMbps()) * hysteria.MbpsToBps,
		Password:           h.config.GetPassword(),
		TLSConfig:          tlsConfig,
		UDPDisabled:        false,
	}
	if err := applyXrayHysteria2Obfs(&clientOptions, h.config); err != nil {
		return nil, err
	}

	client, err := hy2.NewClient(clientOptions)
	if err != nil {
		return nil, fmt.Errorf("hysteria2 xray: create client: %w", err)
	}
	h.client = client
	return client, nil
}

func applyXrayHysteria2Obfs(options *hy2.ClientOptions, config *gen.XrayHysteria2Config) error {
	if config.GetObfsPassword() == "" {
		return nil
	}
	switch strings.ToLower(config.GetObfsType()) {
	case "", hy2.ObfsTypeSalamander:
		options.SalamanderPassword = config.GetObfsPassword()
	case hy2.ObfsTypeGecko:
		options.GeckoPassword = config.GetObfsPassword()
		options.GeckoMinPacketSize = int(config.GetMinPacketSize())
		options.GeckoMaxPacketSize = int(config.GetMaxPacketSize())
	default:
		return fmt.Errorf("hysteria2 xray: unsupported obfs type %q", config.GetObfsType())
	}
	return nil
}

func (h *xrayHysteria2Outbound) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	outbounds := session.OutboundsFromContext(ctx)
	if len(outbounds) == 0 || !outbounds[len(outbounds)-1].Target.IsValid() {
		return xerrors.New("hysteria2 xray: target not specified")
	}
	ob := outbounds[len(outbounds)-1]
	ob.Name = "hysteria2"
	dialer.SetOutboundGateway(ctx, ob)

	client, err := h.getClient(ctx, dialer)
	if err != nil {
		return err
	}
	target := ob.Target
	switch target.Network {
	case xnet.Network_TCP:
		return h.processTCP(ctx, link, client, target)
	case xnet.Network_UDP:
		return h.processUDP(ctx, link, client, target)
	default:
		return xerrors.New("hysteria2 xray: unsupported target network: ", target.Network)
	}
}

func (h *xrayHysteria2Outbound) processTCP(ctx context.Context, link *transport.Link, client *hy2.Client, target xnet.Destination) error {
	conn, err := client.DialConn(ctx, xrayDestinationToSing(target))
	if err != nil {
		return xerrors.New("hysteria2 xray: dial target ", target).Base(err)
	}
	defer conn.Close()

	requestDone := func() error {
		if err := xbuf.Copy(link.Reader, xbuf.NewWriter(conn)); err != nil {
			return xerrors.New("hysteria2 xray: upload failed").Base(err)
		}
		return nil
	}
	responseDone := func() error {
		if err := xbuf.Copy(xbuf.NewReader(conn), link.Writer); err != nil {
			return xerrors.New("hysteria2 xray: download failed").Base(err)
		}
		return nil
	}
	if err := task.Run(ctx, requestDone, task.OnSuccess(responseDone, task.Close(link.Writer))); err != nil {
		return xerrors.New("hysteria2 xray: connection ended").Base(err)
	}
	return nil
}

func (h *xrayHysteria2Outbound) processUDP(ctx context.Context, link *transport.Link, client *hy2.Client, target xnet.Destination) error {
	packetConn, err := client.ListenPacket(ctx)
	if err != nil {
		return xerrors.New("hysteria2 xray: open UDP session").Base(err)
	}
	defer packetConn.Close()

	requestDone := func() error {
		if err := xbuf.Copy(link.Reader, &xrayHysteriaPacketWriter{conn: packetConn, defaultTarget: target}); err != nil {
			return xerrors.New("hysteria2 xray: UDP upload failed").Base(err)
		}
		return nil
	}
	responseDone := func() error {
		if err := xbuf.Copy(&xrayHysteriaPacketReader{conn: packetConn}, link.Writer); err != nil {
			return xerrors.New("hysteria2 xray: UDP download failed").Base(err)
		}
		return nil
	}
	if err := task.Run(ctx, requestDone, task.OnSuccess(responseDone, task.Close(link.Writer))); err != nil {
		return xerrors.New("hysteria2 xray: UDP session ended").Base(err)
	}
	return nil
}

type xrayHysteriaServerDialer struct {
	dialer internet.Dialer
}

func (d *xrayHysteriaServerDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (stdnet.Conn, error) {
	target := singDestinationToXray(destination, network)
	if outbounds := session.OutboundsFromContext(ctx); len(outbounds) == 0 {
		ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: target}})
	}
	return d.dialer.Dial(ctx, target)
}

func (d *xrayHysteriaServerDialer) ListenPacket(context.Context, M.Socksaddr) (stdnet.PacketConn, error) {
	return nil, fmt.Errorf("hysteria2 xray: unconnected packet sockets are not supported")
}

type xrayHysteriaPacketWriter struct {
	conn          stdnet.PacketConn
	defaultTarget xnet.Destination
}

func (w *xrayHysteriaPacketWriter) WriteMultiBuffer(mb xbuf.MultiBuffer) error {
	defer xbuf.ReleaseMulti(mb)
	for _, buffer := range mb {
		target := w.defaultTarget
		if buffer.UDP != nil && buffer.UDP.IsValid() {
			target = *buffer.UDP
		}
		if _, err := w.conn.WriteTo(buffer.Bytes(), xrayDestinationToSing(target)); err != nil {
			return err
		}
	}
	return nil
}

type xrayHysteriaPacketReader struct {
	conn stdnet.PacketConn
}

func (r *xrayHysteriaPacketReader) ReadMultiBuffer() (xbuf.MultiBuffer, error) {
	payload := make([]byte, 65535)
	n, addr, err := r.conn.ReadFrom(payload)
	if err != nil {
		return nil, err
	}
	buffer := xbuf.FromBytes(payload[:n])
	source := M.SocksaddrFromNet(addr)
	destination := singSocksaddrToXray(source, xnet.Network_UDP)
	buffer.UDP = &destination
	return xbuf.MultiBuffer{buffer}, nil
}

func xrayDestinationToSing(destination xnet.Destination) M.Socksaddr {
	if destination.Address.Family().IsDomain() {
		return M.Socksaddr{Fqdn: destination.Address.Domain(), Port: uint16(destination.Port)}
	}
	return M.ParseSocksaddrHostPort(destination.Address.String(), uint16(destination.Port))
}

func singDestinationToXray(destination M.Socksaddr, network string) xnet.Destination {
	xnetwork := xnet.Network_UDP
	if strings.HasPrefix(strings.ToLower(network), "tcp") {
		xnetwork = xnet.Network_TCP
	}
	return singSocksaddrToXray(destination, xnetwork)
}

func singSocksaddrToXray(destination M.Socksaddr, network xnet.Network) xnet.Destination {
	var address xnet.Address
	if destination.IsDomain() {
		address = xnet.DomainAddress(destination.Fqdn)
	} else {
		address = xnet.IPAddress(stdnet.IP(destination.Addr.AsSlice()))
	}
	return xnet.Destination{Network: network, Address: address, Port: xnet.Port(destination.Port)}
}
