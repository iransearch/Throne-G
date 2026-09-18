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

	hyclient "github.com/apernet/hysteria/core/v2/client"
	hyobfs "github.com/apernet/hysteria/extras/v2/obfs"
	sbtls "github.com/sagernet/sing-box/common/tls"
	sblog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	hytransport "github.com/sagernet/sing-quic/hysteria"
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

const (
	xrayHysteria2Protocol = "throne-hysteria2"
	megabitToBytes        = 1_000_000 / 8
)

func init() {
	common.Must(common.RegisterConfig((*gen.XrayHysteria2Config)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return newXrayHysteria2Outbound(ctx, config.(*gen.XrayHysteria2Config))
	}))
}

type xrayHysteria2Outbound struct {
	config *gen.XrayHysteria2Config

	clientMu sync.Mutex
	client   hyclient.Client
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
		return client.Close()
	}
	return nil
}

func (h *xrayHysteria2Outbound) getClient(ctx context.Context, dialer internet.Dialer) (hyclient.Client, error) {
	h.clientMu.Lock()
	defer h.clientMu.Unlock()
	if h.client != nil {
		return h.client, nil
	}

	lifetimeCtx := context.WithoutCancel(ctx)
	configFunc := func() (*hyclient.Config, error) {
		return h.clientConfig(lifetimeCtx, dialer)
	}
	client, err := hyclient.NewReconnectableClient(configFunc, nil, false)
	if err != nil {
		return nil, fmt.Errorf("hysteria2 xray: create Hysteria core v2.12.3 client: %w", err)
	}
	h.client = client
	return client, nil
}

func (h *xrayHysteria2Outbound) clientConfig(ctx context.Context, dialer internet.Dialer) (*hyclient.Config, error) {
	var tlsOptions option.OutboundTLSOptions
	if err := json.Unmarshal([]byte(h.config.GetTlsJson()), &tlsOptions); err != nil {
		return nil, fmt.Errorf("hysteria2 xray: invalid TLS options: %w", err)
	}
	if !tlsOptions.Enabled {
		return nil, fmt.Errorf("hysteria2 xray: TLS is required")
	}
	logger := sblog.NewNOPFactory().Logger()
	singTLSConfig, err := sbtls.NewClient(ctx, logger, h.config.GetServer(), tlsOptions)
	if err != nil {
		return nil, fmt.Errorf("hysteria2 xray: create TLS client: %w", err)
	}
	stdTLSConfig, err := singTLSConfig.STDConfig()
	if err != nil {
		return nil, fmt.Errorf("hysteria2 xray: Hysteria core requires standard Go TLS: %w", err)
	}

	var hopInterval time.Duration
	if value := strings.TrimSpace(h.config.GetHopInterval()); value != "" {
		hopInterval, err = time.ParseDuration(value)
		if err != nil {
			return nil, fmt.Errorf("hysteria2 xray: invalid hop interval %q: %w", value, err)
		}
	}

	serverAddress := M.ParseSocksaddrHostPort(h.config.GetServer(), uint16(h.config.GetServerPort()))
	obfsOptions, err := parseXrayHysteria2Obfs(h.config)
	if err != nil {
		return nil, err
	}

	return &hyclient.Config{
		ConnFactory: &xrayHysteria2ConnFactory{
			ctx:           ctx,
			dialer:        dialer,
			serverAddress: serverAddress,
			serverPorts:   append([]string(nil), h.config.GetServerPorts()...),
			hopInterval:   hopInterval,
			obfs:          obfsOptions,
		},
		ServerAddr: xrayHysteria2ServerAddr{address: serverAddress},
		Auth:       h.config.GetPassword(),
		TLSConfig: hyclient.TLSConfig{
			ServerName:            stdTLSConfig.ServerName,
			InsecureSkipVerify:    stdTLSConfig.InsecureSkipVerify,
			VerifyPeerCertificate: stdTLSConfig.VerifyPeerCertificate,
			RootCAs:               stdTLSConfig.RootCAs,
			GetClientCertificate:  stdTLSConfig.GetClientCertificate,
			ECHConfigList:         stdTLSConfig.EncryptedClientHelloConfigList,
		},
		BandwidthConfig: hyclient.BandwidthConfig{
			MaxTx: uint64(h.config.GetUpMbps()) * megabitToBytes,
			MaxRx: uint64(h.config.GetDownMbps()) * megabitToBytes,
		},
	}, nil
}

type xrayHysteria2ObfsOptions struct {
	typeName      string
	password      string
	minPacketSize int
	maxPacketSize int
}

func parseXrayHysteria2Obfs(config *gen.XrayHysteria2Config) (xrayHysteria2ObfsOptions, error) {
	if config.GetObfsPassword() == "" {
		return xrayHysteria2ObfsOptions{}, nil
	}
	options := xrayHysteria2ObfsOptions{
		typeName: strings.ToLower(config.GetObfsType()),
		password: config.GetObfsPassword(),
	}
	switch options.typeName {
	case "", "salamander":
		options.typeName = "salamander"
	case "gecko":
		options.minPacketSize = int(config.GetMinPacketSize())
		options.maxPacketSize = int(config.GetMaxPacketSize())
	default:
		return xrayHysteria2ObfsOptions{}, fmt.Errorf("hysteria2 xray: unsupported obfs type %q", config.GetObfsType())
	}
	return options, nil
}

func (o xrayHysteria2ObfsOptions) wrap(conn stdnet.PacketConn) (stdnet.PacketConn, error) {
	switch o.typeName {
	case "":
		return conn, nil
	case "salamander":
		return hyobfs.WrapPacketConnSalamander(conn, []byte(o.password))
	case "gecko":
		return hyobfs.WrapPacketConnGecko(conn, hyobfs.GeckoOptions{
			Password:      []byte(o.password),
			MinPacketSize: o.minPacketSize,
			MaxPacketSize: o.maxPacketSize,
		})
	default:
		return nil, fmt.Errorf("hysteria2 xray: unsupported obfs type %q", o.typeName)
	}
}

type xrayHysteria2ConnFactory struct {
	ctx           context.Context
	dialer        internet.Dialer
	serverAddress M.Socksaddr
	serverPorts   []string
	hopInterval   time.Duration
	obfs          xrayHysteria2ObfsOptions
}

func (f *xrayHysteria2ConnFactory) New(stdnet.Addr) (stdnet.PacketConn, error) {
	dial := func(destination M.Socksaddr) (stdnet.Conn, error) {
		target := singSocksaddrToXray(destination, xnet.Network_UDP)
		ctx := f.ctx
		if outbounds := session.OutboundsFromContext(ctx); len(outbounds) == 0 {
			ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: target}})
		}
		return f.dialer.Dial(ctx, target)
	}

	var conn stdnet.Conn
	var err error
	if len(f.serverPorts) == 0 {
		conn, err = dial(f.serverAddress)
	} else {
		ports, parseErr := hytransport.ParsePorts(f.serverPorts)
		if parseErr != nil {
			return nil, fmt.Errorf("hysteria2 xray: invalid server ports: %w", parseErr)
		}
		conn, err = hytransport.NewHopConn(dial, f.serverAddress, ports, f.hopInterval, 0)
	}
	if err != nil {
		return nil, err
	}
	packetConn := &xrayHysteria2ConnectedPacketConn{
		Conn:       conn,
		remoteAddr: xrayHysteria2ServerAddr{address: f.serverAddress},
	}
	wrapped, err := f.obfs.wrap(packetConn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("hysteria2 xray: configure obfs: %w", err)
	}
	return wrapped, nil
}

type xrayHysteria2ServerAddr struct {
	address M.Socksaddr
}

func (a xrayHysteria2ServerAddr) Network() string { return "udp" }
func (a xrayHysteria2ServerAddr) String() string  { return a.address.String() }

type xrayHysteria2ConnectedPacketConn struct {
	stdnet.Conn
	remoteAddr stdnet.Addr
}

func (c *xrayHysteria2ConnectedPacketConn) ReadFrom(payload []byte) (int, stdnet.Addr, error) {
	n, err := c.Read(payload)
	return n, c.remoteAddr, err
}

func (c *xrayHysteria2ConnectedPacketConn) WriteTo(payload []byte, _ stdnet.Addr) (int, error) {
	return c.Write(payload)
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

func (h *xrayHysteria2Outbound) processTCP(ctx context.Context, link *transport.Link, client hyclient.Client, target xnet.Destination) error {
	conn, err := client.TCP(xrayDestinationToSing(target).String())
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

func (h *xrayHysteria2Outbound) processUDP(ctx context.Context, link *transport.Link, client hyclient.Client, target xnet.Destination) error {
	udpConn, err := client.UDP()
	if err != nil {
		return xerrors.New("hysteria2 xray: open UDP session").Base(err)
	}
	defer udpConn.Close()

	requestDone := func() error {
		if err := xbuf.Copy(link.Reader, &xrayHysteriaPacketWriter{conn: udpConn, defaultTarget: target}); err != nil {
			return xerrors.New("hysteria2 xray: UDP upload failed").Base(err)
		}
		return nil
	}
	responseDone := func() error {
		if err := xbuf.Copy(&xrayHysteriaPacketReader{conn: udpConn}, link.Writer); err != nil {
			return xerrors.New("hysteria2 xray: UDP download failed").Base(err)
		}
		return nil
	}
	if err := task.Run(ctx, requestDone, task.OnSuccess(responseDone, task.Close(link.Writer))); err != nil {
		return xerrors.New("hysteria2 xray: UDP session ended").Base(err)
	}
	return nil
}

type xrayHysteriaPacketWriter struct {
	conn          hyclient.HyUDPConn
	defaultTarget xnet.Destination
}

func (w *xrayHysteriaPacketWriter) WriteMultiBuffer(mb xbuf.MultiBuffer) error {
	defer xbuf.ReleaseMulti(mb)
	for _, buffer := range mb {
		target := w.defaultTarget
		if buffer.UDP != nil && buffer.UDP.IsValid() {
			target = *buffer.UDP
		}
		if err := w.conn.Send(buffer.Bytes(), xrayDestinationToSing(target).String()); err != nil {
			return err
		}
	}
	return nil
}

type xrayHysteriaPacketReader struct {
	conn hyclient.HyUDPConn
}

func (r *xrayHysteriaPacketReader) ReadMultiBuffer() (xbuf.MultiBuffer, error) {
	payload, addr, err := r.conn.Receive()
	if err != nil {
		return nil, err
	}
	buffer := xbuf.FromBytes(payload)
	destination := singSocksaddrToXray(M.ParseSocksaddr(addr), xnet.Network_UDP)
	buffer.UDP = &destination
	return xbuf.MultiBuffer{buffer}, nil
}

func xrayDestinationToSing(destination xnet.Destination) M.Socksaddr {
	if destination.Address.Family().IsDomain() {
		return M.Socksaddr{Fqdn: destination.Address.Domain(), Port: uint16(destination.Port)}
	}
	return M.ParseSocksaddrHostPort(destination.Address.String(), uint16(destination.Port))
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
