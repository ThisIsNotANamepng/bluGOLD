package itest

import (
	"context"
	"testing"
	"time"

	"blugold/internal/chain"
	"blugold/internal/crypto"
	"blugold/internal/miner"
	"blugold/internal/node"
)

// mineBlocks extends n's chain by count blocks paying w. TestParams keeps
// difficulty at 1, so any candidate header already meets the target.
func mineBlocks(t *testing.T, n *node.Node, w *crypto.Wallet, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		b := n.BuildCandidate(w.Address())
		if b == nil {
			t.Fatalf("no candidate at height %d", n.TipHeight()+1)
		}
		if err := n.SubmitSolution(b); err != nil {
			t.Fatalf("submit block %d: %v", b.Height, err)
		}
	}
}

// TestDivergedChainsConverge builds two chains that share nothing but genesis,
// then introduces the nodes to each other. Sync must walk back to the common
// ancestor and both nodes must end up on the heavier chain — the failure the
// club hit on the live network, where connected nodes mined side by side
// forever because sync only ever asked for blocks above its own tip height.
func TestDivergedChainsConverge(t *testing.T) {
	wA, _ := crypto.GenerateWallet()
	wB, _ := crypto.GenerateWallet()

	a := startNode(t, "A", nil, wA)
	b := startNode(t, "B", nil, wB)

	mineBlocks(t, a, wA, 5)
	mineBlocks(t, b, wB, 12)

	if a.TipHash() == b.TipHash() {
		t.Fatal("setup: chains should have diverged")
	}
	heavyTip := b.TipHash()
	t.Logf("A on its own chain at height %d (%s), B at height %d (%s)",
		a.TipHeight(), a.TipHash().Short(), b.TipHeight(), heavyTip.Short())

	// Introduce the two nodes. Neither mines from here on: convergence has to
	// come from block sync alone.
	if !b.Switch().AddKnown(a.Switch().Addr()) {
		t.Fatal("AddKnown failed")
	}
	waitFor(t, 20*time.Second, "nodes connect", func() bool {
		return a.Switch().PeerCount() == 1 && b.Switch().PeerCount() == 1
	})

	waitFor(t, 45*time.Second, "A adopts B's heavier chain", func() bool {
		return a.TipHash() == heavyTip
	})
	if a.TipHeight() != 12 {
		t.Fatalf("A height = %d, want 12", a.TipHeight())
	}
	if b.TipHash() != heavyTip {
		t.Fatalf("B abandoned the heavier chain: %s", b.TipHash().Short())
	}

	// The reorg must carry state with it: B's coinbases are now real money and
	// A's abandoned rewards are gone.
	reward := chain.TestParams().RewardAt(1)
	if got := a.GetAccount(wB.Address()).Balance; got != 12*reward {
		t.Fatalf("A credits B with %s BLG, want %s",
			chain.FormatAmount(got), chain.FormatAmount(12*reward))
	}
	if got := a.GetAccount(wA.Address()).Balance; got != 0 {
		t.Fatalf("A still holds %s BLG from its abandoned chain", chain.FormatAmount(got))
	}
	t.Logf("both nodes converged on height %d tip %s", a.TipHeight(), heavyTip.Short())
}

// TestDeepDivergenceSyncsInBatches drives a fork deeper than one sync batch
// (500 blocks) and than the orphan buffer, so convergence depends on the
// requester continuing from where each batch ended rather than from its own
// tip — which never moves while a competing branch is still being downloaded.
func TestDeepDivergenceSyncsInBatches(t *testing.T) {
	if testing.Short() {
		t.Skip("mines 500+ blocks")
	}
	wA, _ := crypto.GenerateWallet()
	wB, _ := crypto.GenerateWallet()

	a := startNode(t, "A", nil, wA)
	b := startNode(t, "B", nil, wB)

	mineBlocks(t, a, wA, 3)
	mineBlocks(t, b, wB, 520)
	heavyTip := b.TipHash()

	if !a.Switch().AddKnown(b.Switch().Addr()) {
		t.Fatal("AddKnown failed")
	}
	waitFor(t, 60*time.Second, "A syncs 520 blocks across batches", func() bool {
		return a.TipHash() == heavyTip
	})
	if a.TipHeight() != 520 {
		t.Fatalf("A height = %d, want 520", a.TipHeight())
	}
	if got := a.GetAccount(wB.Address()).Balance; got != 520*chain.TestParams().RewardAt(1) {
		t.Fatalf("A replayed the branch to balance %s BLG", chain.FormatAmount(got))
	}
	t.Logf("A converged on height %d tip %s", a.TipHeight(), heavyTip.Short())
}

// TestDivergedMinersConverge is the club's live failure in miniature: two
// nodes that mined side by side on separate chains, connected the whole time,
// each ignoring the other's blocks. Once both stop mining they must agree on
// one chain, and the loser's coinbases must be gone from the winner's state.
func TestDivergedMinersConverge(t *testing.T) {
	wA, _ := crypto.GenerateWallet()
	wB, _ := crypto.GenerateWallet()

	a := startNode(t, "A", nil, wA)
	b := startNode(t, "B", nil, wB)
	mineBlocks(t, a, wA, 6)
	mineBlocks(t, b, wB, 14)

	if !b.Switch().AddKnown(a.Switch().Addr()) {
		t.Fatal("AddKnown failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go miner.New(a, wA, 1, 200*time.Millisecond).Run(ctx)
	go miner.New(b, wB, 1, 200*time.Millisecond).Run(ctx)

	waitFor(t, 20*time.Second, "nodes connect", func() bool {
		return a.Switch().PeerCount() == 1 && b.Switch().PeerCount() == 1
	})
	waitFor(t, 45*time.Second, "chains meet", func() bool {
		return a.TipHash() == b.TipHash()
	})
	cancel()

	// Let the last in-flight blocks settle, then require a single agreed tip.
	waitFor(t, 45*time.Second, "nodes agree after mining stops", func() bool {
		return a.TipHash() == b.TipHash() && a.TipHeight() == b.TipHeight()
	})

	tipA, tipB := a.TipHash(), b.TipHash()
	if tipA != tipB {
		t.Fatalf("still forked: A=%s B=%s", tipA.Short(), tipB.Short())
	}
	// Both nodes must value the same coins, on the same chain.
	for _, w := range []*crypto.Wallet{wA, wB} {
		if a.GetAccount(w.Address()).Balance != b.GetAccount(w.Address()).Balance {
			t.Fatalf("balances disagree for %s: A=%s B=%s", w.Address(),
				chain.FormatAmount(a.GetAccount(w.Address()).Balance),
				chain.FormatAmount(b.GetAccount(w.Address()).Balance))
		}
	}
	t.Logf("converged at height %d tip %s", a.TipHeight(), tipA.Short())
}
