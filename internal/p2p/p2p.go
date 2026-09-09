// Package p2p maintains TCP peer connections and transports wire messages.
package p2p

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"blugold/internal/wire"
)

type Peer struct {
	Conn   net.Conn
	Dial   string // key in peers map
	Advert string // peer's advertised listen address
	Height uint64

	mu        sync.Mutex
	handshook bool

	writeMu sync.Mutex
}

func (p *Peer) Send(env *wire.Envelope) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return wire.WriteFrame(p.Conn, env)
}

func (p *Peer) RemoteAddr() string { return p.Conn.RemoteAddr().String() }

func (p *Peer) isHandshook() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handshook
}

func (p *Peer) setHandshook() {
	p.mu.Lock()
	p.handshook = true
	p.mu.Unlock()
}

func (p *Peer) snapshot() (height uint64, handshook bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Height, p.handshook
}

type Switch struct {
	Listen    string // listen address, e.g. ":7007"
	Advertise string // what we tell peers to dial, e.g. "192.168.1.5:7007"
	Seeds     []string
	MaxPeers  int

	GetHeight    func() uint64
	OnMessage    func(p *Peer, env *wire.Envelope)
	OnConnect    func(p *Peer)
	OnDisconnect func(p *Peer)

	mu      sync.Mutex
	peers   map[string]*Peer
	known   map[string]bool
	persist func(addrs []string)
	ln      net.Listener
	closed  bool
	ctxDone chan struct{}
	wg      sync.WaitGroup
}

func New(listen, advertise string, seeds []string) *Switch {
	if advertise == "" {
		advertise = listen
	}
	s := &Switch{
		Listen:    listen,
		Advertise: advertise,
		Seeds:     seeds,
		MaxPeers:  32,
		peers:     make(map[string]*Peer),
		known:     make(map[string]bool),
		ctxDone:   make(chan struct{}),
	}
	for _, seed := range seeds {
		if seed == "" || seed == listen || seed == advertise {
			continue
		}
		s.known[seed] = true
	}
	return s
}

func (s *Switch) Addr() string {
	if s.ln == nil {
		return s.Listen
	}
	return s.ln.Addr().String()
}

func (s *Switch) AddKnown(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if addr == "" || s.closed {
		return false
	}
	if addr == s.Advertise || addr == s.Listen {
		return false
	}
	if s.ln != nil && addr == s.ln.Addr().String() {
		return false
	}
	if s.known[addr] {
		return false
	}
	s.known[addr] = true
	return true
}

func (s *Switch) KnownAddrs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.known))
	for a := range s.known {
		out = append(out, a)
	}
	return out
}

func (s *Switch) Peers() []*Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Peer, 0, len(s.peers))
	for _, p := range s.peers {
		out = append(out, p)
	}
	return out
}

func (s *Switch) Start() error {
	ln, err := net.Listen("tcp", s.Listen)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.Advertise == s.Listen {
		s.Advertise = ln.Addr().String()
	}
	s.mu.Unlock()
	s.ln = ln
	s.wg.Add(3)
	go s.acceptLoop()
	go s.dialLoop()
	go s.peersLoop()
	return nil
}

func (s *Switch) Stop() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.ctxDone)
	for _, p := range s.peers {
		p.Conn.Close()
	}
	s.mu.Unlock()
	if s.ln != nil {
		s.ln.Close()
	}
}

func (s *Switch) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Switch) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.ctxDone:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if s.full() {
			conn.Close()
			continue
		}
		s.wg.Add(1)
		go s.serveConn(conn, "")
	}
}

func (s *Switch) full() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.peers) >= s.MaxPeers
}

func (s *Switch) dialLoop() {
	defer s.wg.Done()
	dialPass := func() {
		if s.full() {
			return
		}
		s.mu.Lock()
		var candidates []string
		for a := range s.known {
			if _, ok := s.peers[a]; !ok {
				candidates = append(candidates, a)
			}
		}
		s.mu.Unlock()
		if len(candidates) == 0 {
			return
		}
		rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
		for _, addr := range candidates {
			if s.isClosed() || s.full() {
				return
			}
			conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
			if err != nil {
				continue
			}
			s.wg.Add(1)
			go s.serveConn(conn, addr)
		}
	}
	dialPass()
	for {
		select {
		case <-s.ctxDone:
			return
		case <-time.After(3 * time.Second):
		}
		dialPass()
	}
}

func (s *Switch) serveConn(conn net.Conn, dialAddr string) {
	defer s.wg.Done()
	peer := &Peer{Conn: conn, Dial: dialAddr}
	if peer.Dial == "" {
		peer.Dial = conn.RemoteAddr().String()
	}

	ourVersion, err := wire.NewEnvelope(wire.MsgVersion, wire.VersionMsg{
		Protocol:   wire.ProtocolVersion,
		ListenAddr: s.Advertise,
		Height:     s.heightSafe(),
	})
	if err != nil {
		conn.Close()
		return
	}
	if err := peer.Send(ourVersion); err != nil {
		conn.Close()
		return
	}

	for {
		env, err := wire.ReadFrame(conn)
		if err != nil {
			s.dropPeer(peer)
			return
		}
		if !peer.isHandshook() {
			if env.Type != wire.MsgVersion {
				s.dropPeer(peer)
				return
			}
			var v wire.VersionMsg
			if err := json.Unmarshal(env.Payload, &v); err != nil || v.Protocol != wire.ProtocolVersion {
				s.dropPeer(peer)
				return
			}
			if s.isSelfAddr(v.ListenAddr) {
				s.dropPeer(peer) // self connection
				return
			}
			peer.Advert = v.ListenAddr
			peer.Height = v.Height
			peer.setHandshook()
			if !s.register(peer) {
				s.dropPeer(peer)
				return
			}
			s.addKnownFromPeer(v.ListenAddr)
			if s.OnConnect != nil {
				s.OnConnect(peer)
			}
			continue
		}
		if env.Type == wire.MsgPeers {
			s.handlePeersMsg(peer, env)
			continue
		}
		if s.OnMessage != nil {
			s.OnMessage(peer, env)
		}
	}
}

func (s *Switch) heightSafe() uint64 {
	if s.GetHeight == nil {
		return 0
	}
	return s.GetHeight()
}

func (s *Switch) isSelfAddr(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if addr == s.Advertise || addr == s.Listen {
		return true
	}
	return s.ln != nil && addr == s.ln.Addr().String()
}

func (s *Switch) register(p *Peer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	for _, existing := range s.peers {
		if existing.Advert == p.Advert {
			return false
		}
	}
	if _, ok := s.peers[p.Dial]; ok {
		return false
	}
	s.peers[p.Dial] = p
	return true
}

func (s *Switch) dropPeer(p *Peer) {
	p.Conn.Close()
	s.mu.Lock()
	had := s.peers[p.Dial] == p
	if had {
		delete(s.peers, p.Dial)
	}
	s.mu.Unlock()
	if had && s.OnDisconnect != nil {
		s.OnDisconnect(p)
	}
}

func (s *Switch) addKnownFromPeer(advert string) {
	if s.AddKnown(advert) {
		s.persistPeers()
	}
}

// SetPeerPersister wires peer persistence (usually to store.SavePeers).
func (s *Switch) SetPeerPersister(f func(addrs []string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persist = f
}

func (s *Switch) persistPeers() {
	s.mu.Lock()
	f := s.persist
	s.mu.Unlock()
	if f != nil {
		f(s.KnownAddrs())
	}
}

func (s *Switch) handlePeersMsg(p *Peer, env *wire.Envelope) {
	var msg wire.PeersMsg
	if json.Unmarshal(env.Payload, &msg) != nil {
		return
	}
	added := false
	for _, a := range msg.Addrs {
		if s.AddKnown(a) {
			added = true
		}
	}
	if added {
		s.persistPeers()
	}
}

func (s *Switch) peersLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctxDone:
			return
		case <-ticker.C:
		}
		addrs := s.KnownAddrs()
		if len(addrs) > 64 {
			addrs = addrs[:64]
		}
		if len(addrs) == 0 {
			continue
		}
		if env, err := wire.NewEnvelope(wire.MsgPeers, wire.PeersMsg{Addrs: addrs}); err == nil {
			s.Broadcast(env, "")
		}
	}
}

func (s *Switch) Broadcast(env *wire.Envelope, exceptDial string) {
	for _, p := range s.Peers() {
		if p.Dial == exceptDial || !p.isHandshook() {
			continue
		}
		go func(p *Peer) {
			_ = p.Send(env)
		}(p)
	}
}

func (s *Switch) PeerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.peers)
}

func (s *Switch) BestPeerHeight() (uint64, *Peer) {
	var best *Peer
	var h uint64
	for _, p := range s.Peers() {
		height, ok := p.snapshot()
		if !ok {
			continue
		}
		if height > h {
			h = height
			best = p
		}
	}
	return h, best
}

func (s *Switch) String() string {
	return fmt.Sprintf("switch[%s peers=%d]", s.Listen, s.PeerCount())
}
