package p2p

import (
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"blugold/internal/chain"
	"blugold/internal/wire"
)

func startSwitch(t *testing.T, listen string) *Switch {
	t.Helper()
	s := New(listen, listen, nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

func portOf(t *testing.T, s *Switch) string {
	t.Helper()
	_, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func waitFor(t *testing.T, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestTwoSwitchesHandshakeAndBroadcast(t *testing.T) {
	a := startSwitch(t, "127.0.0.1:0")
	b := startSwitch(t, "127.0.0.1:0")

	var mu sync.Mutex
	var gotMsg *wire.Envelope
	done := make(chan struct{})
	a.OnMessage = func(p *Peer, env *wire.Envelope) {
		mu.Lock()
		gotMsg = env
		mu.Unlock()
		close(done)
	}

	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+portOf(t, a), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b.wg.Add(1)
	go b.serveConn(conn, conn.RemoteAddr().String())

	waitFor(t, 3*time.Second, func() bool {
		return b.PeerCount() == 1 && a.PeerCount() == 1
	})

	env, _ := wire.NewEnvelope(wire.MsgGetBlocks, wire.GetBlocksMsg{Locator: []chain.Hash{chain.HashBytes([]byte("tip"))}, Count: 3})
	b.Broadcast(env, "")

	select {
	case <-done:
		mu.Lock()
		defer mu.Unlock()
		if gotMsg.Type != wire.MsgGetBlocks {
			t.Fatalf("got %s", gotMsg.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("message never arrived")
	}
}

func TestPeersGossipLearned(t *testing.T) {
	a := startSwitch(t, "127.0.0.1:0")
	b := startSwitch(t, "127.0.0.1:0")

	if !a.AddKnown("203.0.113.9:9999") {
		t.Fatal("AddKnown failed")
	}

	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+portOf(t, a), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b.wg.Add(1)
	go b.serveConn(conn, conn.RemoteAddr().String())

	waitFor(t, 3*time.Second, func() bool {
		return b.PeerCount() == 1 && a.PeerCount() == 1
	})

	env, _ := wire.NewEnvelope(wire.MsgPeers, wire.PeersMsg{Addrs: a.KnownAddrs()})
	a.Broadcast(env, "")

	waitFor(t, 3*time.Second, func() bool {
		for _, k := range b.KnownAddrs() {
			if k == "203.0.113.9:9999" {
				return true
			}
		}
		return false
	})
}

func TestDuplicateAdvertDropped(t *testing.T) {
	a := startSwitch(t, "127.0.0.1:0")
	b := startSwitch(t, "127.0.0.1:0")
	aPort := portOf(t, a)

	conn1, err := net.DialTimeout("tcp", "127.0.0.1:"+aPort, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b.wg.Add(1)
	go b.serveConn(conn1, conn1.RemoteAddr().String())

	waitFor(t, 3*time.Second, func() bool {
		return a.PeerCount() == 1 && b.PeerCount() == 1
	})

	conn2, err := net.DialTimeout("tcp", "127.0.0.1:"+aPort, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b.wg.Add(1)
	go b.serveConn(conn2, conn2.RemoteAddr().String())

	waitFor(t, 3*time.Second, func() bool {
		return a.PeerCount() == 1 && b.PeerCount() == 1
	})

	conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	for {
		_, err := conn2.Read(buf)
		if err == nil {
			continue
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatal("duplicate connection was not closed")
		}
		break
	}
}

func TestSeedDialing(t *testing.T) {
	a := startSwitch(t, "127.0.0.1:0")
	bPort := portOf(t, a)

	b := New("127.0.0.1:0", "127.0.0.1:0", []string{"127.0.0.1:" + bPort})
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)

	waitFor(t, 8*time.Second, func() bool {
		return a.PeerCount() == 1 && b.PeerCount() == 1
	})
}

func TestNoSelfConnection(t *testing.T) {
	a := startSwitch(t, "127.0.0.1:0")
	b := New("127.0.0.1:0", "127.0.0.1:0", []string{"127.0.0.1:" + portOf(t, a)})
	b.Advertise = "203.0.113.8:7007" // different from a; identity is the nonce
	b.Nonce = a.Nonce
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)

	time.Sleep(4 * time.Second)
	if a.PeerCount() != 0 {
		t.Fatalf("self connection accepted: %d peers", a.PeerCount())
	}
}

// Two distinct nodes can advertise the same public address (mis-set --adv,
// or two processes on one host). Matching advert must not look like a
// self-dial when the nonces differ.
func TestDistinctNonceNotSelfDespiteSharedPublicAdvert(t *testing.T) {
	const shared = "203.0.113.7:7007"
	a := New("127.0.0.1:0", shared, nil)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)

	b := New("127.0.0.1:0", shared, []string{a.Addr()})
	if a.Nonce == b.Nonce {
		t.Fatal("nonces collided")
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)

	waitFor(t, 8*time.Second, func() bool {
		return a.PeerCount() == 1 && b.PeerCount() == 1
	})
}

// Two home/campus nodes behind NAT typically advertise the same RFC1918
// address (192.168.1.5:7007 is extremely common). The seed used to treat
// that advert as an identity and drop everyone after the first friend, so
// coins never relayed. They must both stay connected, and a message from
// one must reach the other through the seed.
func TestSeedKeepsNATClientsWithSharedAdvert(t *testing.T) {
	seed := startSwitch(t, "127.0.0.1:0")
	seedAddr := seed.Addr()

	const sharedLAN = "192.168.1.5:7007"
	a := New("127.0.0.1:0", sharedLAN, []string{seedAddr})
	b := New("127.0.0.1:0", sharedLAN, []string{seedAddr})
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)

	waitFor(t, 8*time.Second, func() bool {
		return seed.PeerCount() == 2 && a.PeerCount() == 1 && b.PeerCount() == 1
	})

	seed.OnMessage = func(from *Peer, env *wire.Envelope) {
		seed.Broadcast(env, from.Dial)
	}
	got := make(chan *wire.Envelope, 1)
	b.OnMessage = func(_ *Peer, env *wire.Envelope) {
		select {
		case got <- env:
		default:
		}
	}

	env, err := wire.NewEnvelope(wire.MsgNewTx, wire.NewTxMsg{})
	if err != nil {
		t.Fatal(err)
	}
	a.Broadcast(env, "")

	select {
	case msg := <-got:
		if msg.Type != wire.MsgNewTx {
			t.Fatalf("got %s", msg.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("seed did not relay between NAT clients that share an advert")
	}
}

func TestAddKnownRejectsUnspecified(t *testing.T) {
	s := startSwitch(t, "127.0.0.1:0")
	for _, addr := range []string{":7007", "0.0.0.0:7007", "[::]:7007"} {
		if s.AddKnown(addr) {
			t.Fatalf("AddKnown(%q) accepted unspecified address", addr)
		}
	}
	if !s.AddKnown("203.0.113.9:7007") {
		t.Fatal("AddKnown rejected a public address")
	}
}

func TestPublicDialable(t *testing.T) {
	if isPublicDialable("192.168.1.5:7007") || isPublicDialable("10.0.0.2:7007") || isPublicDialable("127.0.0.1:7007") || isPublicDialable("0.0.0.0:7007") || isPublicDialable(":7007") {
		t.Fatal("private/unspecified treated as public")
	}
	if !isPublicDialable("203.0.113.7:7007") {
		t.Fatal("public IP not treated as public")
	}
	if isUnspecifiedAddr("192.168.1.5:7007") || !isUnspecifiedAddr("0.0.0.0:7007") || !isUnspecifiedAddr(":7007") {
		t.Fatal("isUnspecifiedAddr")
	}
}
