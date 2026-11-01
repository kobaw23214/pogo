package browserrelay

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/pion/turn/v5"
)

type relayGenerator struct{ s *Service }

func (r *relayGenerator) Validate() error { return nil }

func (r *relayGenerator) AllocatePacketConn(cfg turn.AllocateListenerConfig) (net.PacketConn, net.Addr, error) {
	if cfg.Network != "udp4" || cfg.RequestedPort != 0 {
		return nil, nil, ErrPeerDenied
	}
	r.s.mu.Lock()
	g := r.s.grants[cfg.UserID]
	if g == nil || g.pinned == nil || g.revoked.Load() || !time.Now().Before(g.leaseUntil) || cfg.Realm != r.s.cfg.Realm || len(g.packets) != 0 || r.s.closed || r.s.draining {
		r.s.mu.Unlock()
		return nil, nil, ErrPeerDenied
	}
	conn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(r.s.cfg.BindAddr, 0)))
	if err != nil {
		r.s.mu.Unlock()
		return nil, nil, err
	}
	p := &relayPacketConn{PacketConn: conn, s: r.s, g: g}
	g.packets[p] = struct{}{}
	r.s.mu.Unlock()
	local := conn.LocalAddr().(*net.UDPAddr)
	advertised := net.UDPAddrFromAddrPort(netip.AddrPortFrom(r.s.cfg.RelayAddr, uint16(local.Port)))
	return p, advertised, nil
}

func (*relayGenerator) AllocateListener(turn.AllocateListenerConfig) (net.Listener, net.Addr, error) {
	return nil, nil, errors.New("browser relay supports UDP allocations over TCP only")
}

func (*relayGenerator) AllocateConn(turn.AllocateConnConfig) (net.Conn, error) {
	return nil, errors.New("browser relay does not permit peer TCP connections")
}

type relayPacketConn struct {
	net.PacketConn
	s    *Service
	g    *grant
	once sync.Once
}

func (p *relayPacketConn) allowed(addr net.Addr) bool {
	if p.g.revoked.Load() || !time.Now().Before(p.g.leaseUntil) {
		return false
	}
	udp, ok := addr.(*net.UDPAddr)
	if !ok {
		return false
	}
	peer := udp.AddrPort()
	_, ok = p.g.peers[netip.AddrPortFrom(peer.Addr().Unmap(), peer.Port())]
	return ok
}

func (p *relayPacketConn) WriteTo(data []byte, addr net.Addr) (int, error) {
	if !p.allowed(addr) {
		return 0, ErrPeerDenied
	}
	return p.PacketConn.WriteTo(data, addr)
}

func (p *relayPacketConn) ReadFrom(data []byte) (int, net.Addr, error) {
	for {
		if p.g.revoked.Load() || !time.Now().Before(p.g.leaseUntil) {
			_ = p.Close()
			return 0, nil, net.ErrClosed
		}
		n, addr, err := p.PacketConn.ReadFrom(data)
		if err != nil {
			return n, addr, err
		}
		if p.allowed(addr) {
			return n, addr, nil
		}
	}
}

func (p *relayPacketConn) Close() error {
	var err error
	p.once.Do(func() {
		err = p.PacketConn.Close()
		p.s.mu.Lock()
		delete(p.g.packets, p)
		p.s.mu.Unlock()
	})
	return err
}
