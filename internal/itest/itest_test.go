// Package itest runs a real multi-node network over loopback TCP.
package itest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"blugold/internal/chain"
	"blugold/internal/crypto"
	"blugold/internal/miner"
	"blugold/internal/node"
)

func startNode(t *testing.T, name string, seeds []string, w *crypto.Wallet) *node.Node {
	t.Helper()
	n, err := node.New(node.Config{
		Params:    chain.TestParams(),
		DataDir:   t.TempDir(),
		Listen:    "127.0.0.1:0",
		Seeds:     seeds,
		Wallet:    w,
		PeerSync:  true,
		LogBlocks: true,
		MaxPeers:  8,
	})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	t.Cleanup(n.Stop)
	return n
}

func waitFor(t *testing.T, d time.Duration, what string, f func() bool, args ...any) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: condition not met in %v", fmt.Sprintf(what, args...), d)
}

func TestThreeNodeNetwork(t *testing.T) {
	wA, _ := crypto.GenerateWallet()
	wB, _ := crypto.GenerateWallet()
	wC, _ := crypto.GenerateWallet()

	a := startNode(t, "A", nil, wA)
	b := startNode(t, "B", []string{a.Switch().Addr()}, wB)
	c := startNode(t, "C", []string{a.Switch().Addr()}, wC)

	m := miner.New(a, wA, 1, 150*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	waitFor(t, 15*time.Second, "A mines to height 5", func() bool { return a.TipHeight() >= 5 })
	waitFor(t, 30*time.Second, "B syncs to height 5", func() bool { return b.TipHeight() >= 5 })
	waitFor(t, 30*time.Second, "C syncs to height 5", func() bool { return c.TipHeight() >= 5 })

	if a.TipHash() != b.TipHash() {
		t.Fatalf("tips diverge: A=%s B=%s", a.TipHash().Short(), b.TipHash().Short())
	}
	t.Logf("network at height %d, tip %s", a.TipHeight(), a.TipHash().Short())

	txid, err := a.Send(wC.Address(), 1e8, 0)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	t.Logf("sent tx %s from A to C (1 BLG)", txid.Short())

	waitFor(t, 30*time.Second, "C sees balance 1 BLG", func() bool {
		return c.GetAccount(wC.Address()).Balance >= 1e8
	})
	waitFor(t, 15*time.Second, "A confirms spend", func() bool {
		return a.MempoolSize() == 0
	})

	if c.TipHash() != a.TipHash() {
		t.Fatalf("tips diverge after tx: A=%s C=%s", a.TipHash().Short(), c.TipHash().Short())
	}
	if balB := b.GetAccount(wC.Address()).Balance; balB < 1e8 {
		t.Fatalf("B does not see C's balance: %d", balB)
	}
	t.Log("payment confirmed across network")
}

func TestLateJoinerCatchesUp(t *testing.T) {
	wA, _ := crypto.GenerateWallet()
	wD, _ := crypto.GenerateWallet()

	a := startNode(t, "A", nil, wA)
	m := miner.New(a, wA, 1, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	waitFor(t, 15*time.Second, "A mines to height 8", func() bool { return a.TipHeight() >= 8 })

	d := startNode(t, "D", []string{a.Switch().Addr()}, wD)
	waitFor(t, 30*time.Second, "late joiner D catches up", func() bool {
		return d.TipHeight() >= a.TipHeight()-1 && d.TipHash() == a.TipHash()
	})
	t.Logf("late joiner synced to height %d", d.TipHeight())
}
