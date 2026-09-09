package miner

import (
	"context"
	"testing"
	"time"

	"blugold/internal/chain"
	"blugold/internal/crypto"
	"blugold/internal/node"
)

func TestMinerProducesBlocks(t *testing.T) {
	w, err := crypto.GenerateWallet()
	if err != nil {
		t.Fatal(err)
	}
	n, err := node.New(node.Config{
		Params:  chain.TestParams(),
		DataDir: t.TempDir(),
		Wallet:  w,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Stop()

	m := New(n, w, 2, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if n.TipHeight() >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n.TipHeight() < 3 {
		t.Fatalf("miner produced only %d blocks", n.TipHeight())
	}
	bal := n.GetAccount(w.Address()).Balance
	if bal == 0 {
		t.Fatal("miner got no rewards")
	}
	if m.Hashrate() == 0 && n.TipHeight() >= 3 {
		t.Log("hashrate counter idle at sample time")
	}
}
