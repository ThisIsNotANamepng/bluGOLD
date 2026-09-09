package p2p

import (
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

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

	env, _ := wire.NewEnvelope(wire.MsgGetBlocks, wire.GetBlocksMsg{From: 7, Count: 3})
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
	b.Advertise = a.Advertise // pretend to be the same node
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)

	time.Sleep(4 * time.Second)
	if a.PeerCount() != 0 {
		t.Fatalf("self connection accepted: %d peers", a.PeerCount())
	}
}
