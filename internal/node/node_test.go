package node

import (
	"testing"

	"blugold/internal/chain"
	"blugold/internal/crypto"
	"blugold/internal/state"
)

func newNode(t *testing.T) (*Node, *crypto.Wallet) {
	t.Helper()
	w, err := crypto.GenerateWallet()
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(Config{
		Params:   chain.TestParams(),
		DataDir:  t.TempDir(),
		Wallet:   w,
		PeerSync: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Stop)
	return n, w
}

func mineOn(t *testing.T, n *Node, w *crypto.Wallet, parent *blockEntry) *chain.Block {
	t.Helper()
	b := &chain.Block{
		Height:     parent.block.Height + 1,
		PrevHash:   parent.hash,
		Time:       parent.block.Time + 1000,
		Difficulty: 1,
		Miner:      w.Address(),
	}
	cb := &chain.Tx{From: crypto.CoinbaseAddress, To: w.Address(), Amount: chain.TestParams().RewardAt(b.Height), Nonce: b.Height, Time: b.Time}
	b.Txs = []*chain.Tx{cb}
	b.MerkleRoot = b.ComputeMerkleRoot()
	if err := n.SubmitSolution(b); err != nil {
		t.Fatalf("mine/submit block %d: %v", b.Height, err)
	}
	return b
}

func tipEntry(t *testing.T, n *Node) *blockEntry {
	t.Helper()
	h := n.TipHash()
	n.mu.Lock()
	defer n.mu.Unlock()
	e, ok := n.blocks[h]
	if !ok {
		t.Fatal("tip entry missing")
	}
	return e
}

func TestGenesisBoot(t *testing.T) {
	n, _ := newNode(t)
	if n.TipHeight() != 0 {
		t.Fatalf("tip height = %d", n.TipHeight())
	}
	if n.TipHash() != n.genesisHash() {
		t.Fatal("tip should be genesis")
	}
}

func TestMineAndPersist(t *testing.T) {
	dir := t.TempDir()
	w, _ := crypto.GenerateWallet()
	n, err := New(Config{Params: chain.TestParams(), DataDir: dir, Wallet: w})
	if err != nil {
		t.Fatal(err)
	}
	mineOn(t, n, w, tipEntry(t, n))
	mineOn(t, n, w, tipEntry(t, n))
	if n.TipHeight() != 2 {
		t.Fatalf("height = %d", n.TipHeight())
	}
	bal := n.GetAccount(w.Address()).Balance
	want := chain.TestParams().RewardAt(1) + chain.TestParams().RewardAt(2)
	if bal != want {
		t.Fatalf("balance = %d, want %d", bal, want)
	}
	n.Stop()

	n2, err := New(Config{Params: chain.TestParams(), DataDir: dir, Wallet: w})
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Stop()
	if n2.TipHeight() != 2 || n2.TipHash() != n.TipHash() {
		t.Fatalf("reload mismatch: height %d", n2.TipHeight())
	}
	if n2.GetAccount(w.Address()).Balance != want {
		t.Fatal("reloaded balance mismatch")
	}
}

func TestReorg(t *testing.T) {
	n, w := newNode(t)
	w2, _ := crypto.GenerateWallet()

	g := tipEntry(t, n)

	a1 := forkBlock(t, n, w, g, "A")
	if err := n.SubmitSolution(a1); err != nil {
		t.Fatal(err)
	}
	a2 := forkBlock(t, n, w, blockEntryOf(t, n, a1), "A")
	if err := n.SubmitSolution(a2); err != nil {
		t.Fatal(err)
	}
	aTipHash := n.TipHash()
	aTipHeight := n.TipHeight()

	b1 := forkBlock(t, n, w, g, "B")
	b1.Miner = w2.Address()
	b1.Txs = []*chain.Tx{{From: crypto.CoinbaseAddress, To: w2.Address(), Amount: chain.TestParams().RewardAt(1), Nonce: 1, Time: b1.Time}}
	b1.MerkleRoot = b1.ComputeMerkleRoot()
	if err := n.SubmitSolution(b1); err != nil {
		t.Fatal(err)
	}
	if n.TipHash() != aTipHash {
		t.Fatal("lower-work fork should not win")
	}

	b2 := forkBlock(t, n, w, blockEntryOf(t, n, b1), "B")
	b2.Miner = w2.Address()
	b2.Txs = []*chain.Tx{{From: crypto.CoinbaseAddress, To: w2.Address(), Amount: chain.TestParams().RewardAt(2), Nonce: 2, Time: b2.Time}}
	b2.MerkleRoot = b2.ComputeMerkleRoot()
	if err := n.SubmitSolution(b2); err != nil {
		t.Fatal(err)
	}

	b3 := forkBlock(t, n, w, blockEntryOf(t, n, b2), "B")
	b3.Miner = w2.Address()
	b3.Txs = []*chain.Tx{{From: crypto.CoinbaseAddress, To: w2.Address(), Amount: chain.TestParams().RewardAt(3), Nonce: 3, Time: b3.Time}}
	b3.MerkleRoot = b3.ComputeMerkleRoot()
	if err := n.SubmitSolution(b3); err != nil {
		t.Fatal(err)
	}
	if n.TipHeight() != aTipHeight+1 {
		t.Fatalf("after reorg tip height = %d, want %d", n.TipHeight(), aTipHeight+1)
	}
	if got := n.GetAccount(w2.Address()).Balance; got != chain.TestParams().RewardAt(1)+chain.TestParams().RewardAt(2)+chain.TestParams().RewardAt(3) {
		t.Fatalf("w2 balance = %d", got)
	}
	if got := n.GetAccount(w.Address()).Balance; got != 0 {
		t.Fatalf("w balance after rollback should be 0, got %d", got)
	}
}

func blockEntryOf(t *testing.T, n *Node, b *chain.Block) *blockEntry {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	e := n.blocks[b.Hash()]
	if e == nil {
		t.Fatal("block missing from tree")
	}
	return e
}

func forkBlock(t *testing.T, n *Node, w *crypto.Wallet, parent *blockEntry, tag string) *chain.Block {
	t.Helper()
	b := &chain.Block{
		Height:     parent.block.Height + 1,
		PrevHash:   parent.hash,
		Time:       parent.block.Time + 1000,
		Difficulty: 1,
		Miner:      w.Address(),
	}
	cb := &chain.Tx{From: crypto.CoinbaseAddress, To: w.Address(), Amount: chain.TestParams().RewardAt(b.Height), Nonce: b.Height, Time: b.Time}
	b.Txs = []*chain.Tx{cb}
	b.MerkleRoot = b.ComputeMerkleRoot()
	return b
}

func TestReorgHeavierWins(t *testing.T) {
	n, w := newNode(t)
	w2, _ := crypto.GenerateWallet()
	g := tipEntry(t, n)

	a1 := forkBlock(t, n, w, g, "A")
	if err := n.SubmitSolution(a1); err != nil {
		t.Fatal(err)
	}
	aHash := n.TipHash()

	b1 := forkBlock(t, n, w2, g, "B")
	if err := n.SubmitSolution(b1); err != nil {
		t.Fatal(err)
	}
	b2 := forkBlock(t, n, w2, blockEntryOf(t, n, b1), "B")
	if err := n.SubmitSolution(b2); err != nil {
		t.Fatal(err)
	}

	if n.TipHash() == aHash {
		t.Fatal("heavier fork should win")
	}
	if n.TipHeight() != 2 {
		t.Fatalf("height = %d", n.TipHeight())
	}
	if got := n.GetAccount(w2.Address()).Balance; got != chain.TestParams().RewardAt(1)+chain.TestParams().RewardAt(2) {
		t.Fatalf("w2 balance after reorg = %d", got)
	}
	if got := n.GetAccount(w.Address()).Balance; got != 0 {
		t.Fatalf("w balance after rollback should be 0, got %d", got)
	}
}

func TestMempoolFlow(t *testing.T) {
	n, w := newNode(t)
	w2, _ := crypto.GenerateWallet()

	s := state.New()
	s.Credit(w.Address(), 10e8)
	n.mu.Lock()
	n.state = s
	n.pendingDirty = true
	n.mu.Unlock()

	tx := &chain.Tx{From: w.Address(), To: w2.Address(), Amount: 4e8, Fee: 0, Nonce: 0, Time: 1, PubKey: w.PubKey()}
	tx.Sig = w.Sign(tx.SigningBytes())
	if err := n.AddTx(tx, ""); err != nil {
		t.Fatal(err)
	}
	if n.MempoolSize() != 1 {
		t.Fatalf("mempool = %d", n.MempoolSize())
	}
	if err := n.AddTx(tx, ""); err != nil {
		t.Fatalf("duplicate add should be noop: %v", err)
	}
	if n.MempoolSize() != 1 {
		t.Fatal("duplicate tx re-added")
	}

	b := forkBlock(t, n, w, tipEntry(t, n), "M")
	b.Txs[0].Amount = chain.TestParams().RewardAt(1)
	cbTo := b.Txs[0]
	_ = cbTo
	b.Txs = []*chain.Tx{b.Txs[0], tx}
	b.Txs[0].Amount = chain.TestParams().RewardAt(1)
	b.MerkleRoot = b.ComputeMerkleRoot()
	if err := n.SubmitSolution(b); err != nil {
		t.Fatal(err)
	}
	if n.MempoolSize() != 0 {
		t.Fatalf("mempool should be empty after confirm, = %d", n.MempoolSize())
	}
	if n.GetAccount(w2.Address()).Balance != 4e8 {
		t.Fatal("recipient balance missing")
	}
}

func TestMempoolRollbackRestore(t *testing.T) {
	n, w := newNode(t)
	w2, _ := crypto.GenerateWallet()

	s := state.New()
	s.Credit(w.Address(), 10e8)
	n.mu.Lock()
	n.state = s
	n.pendingDirty = true
	n.mu.Unlock()

	tx := &chain.Tx{From: w.Address(), To: w2.Address(), Amount: 1e8, Nonce: 0, Time: 1, PubKey: w.PubKey()}
	tx.Sig = w.Sign(tx.SigningBytes())
	if err := n.AddTx(tx, ""); err != nil {
		t.Fatal(err)
	}

	genesisEntry := tipEntry(t, n)
	b := forkBlock(t, n, w, genesisEntry, "R")
	b.Txs = append(b.Txs, tx)
	b.Txs[0].Amount += tx.Fee
	b.MerkleRoot = b.ComputeMerkleRoot()
	if err := n.SubmitSolution(b); err != nil {
		t.Fatal(err)
	}
	if n.MempoolSize() != 0 {
		t.Fatalf("confirmed tx still in mempool: %d", n.MempoolSize())
	}

	fork1 := forkBlock(t, n, w, genesisEntry, "F1")
	fork1.MerkleRoot = fork1.ComputeMerkleRoot()
	if err := n.SubmitSolution(fork1); err != nil {
		t.Fatal(err)
	}
	fork2 := forkBlock(t, n, w, blockEntryOf(t, n, fork1), "F2")
	if err := n.SubmitSolution(fork2); err != nil {
		t.Fatal(err)
	}

	if n.TipHash() != fork2.Hash() {
		t.Fatalf("heavier fork should win, tip=%s", n.TipHash().Short())
	}
	if n.MempoolSize() != 1 {
		t.Fatalf("rolled-back tx should return to mempool, mempool=%d", n.MempoolSize())
	}

	b3 := forkBlock(t, n, w, blockEntryOf(t, n, fork2), "F3")
	b3.Txs = append(b3.Txs, tx)
	b3.Txs[0].Amount += tx.Fee
	b3.MerkleRoot = b3.ComputeMerkleRoot()
	if err := n.SubmitSolution(b3); err != nil {
		t.Fatal(err)
	}
	if n.MempoolSize() != 0 {
		t.Fatalf("re-confirmed tx should leave mempool, mempool=%d", n.MempoolSize())
	}
	if n.GetAccount(w2.Address()).Balance != 1e8 {
		t.Fatalf("recipient balance = %d", n.GetAccount(w2.Address()).Balance)
	}
	if got := n.GetAccount(w.Address()).Balance; got != 2e8 {
		t.Fatalf("sender balance = %d, want 2 BLG", got)
	}
}

func TestOrphanPromotion(t *testing.T) {
	n, w := newNode(t)
	g := tipEntry(t, n)

	child := &chain.Block{
		Height:     1,
		PrevHash:   g.hash,
		Time:       g.block.Time + 1000,
		Difficulty: 1,
		Miner:      w.Address(),
	}
	cb := &chain.Tx{From: crypto.CoinbaseAddress, To: w.Address(), Amount: chain.TestParams().RewardAt(1), Nonce: 1, Time: child.Time}
	child.Txs = []*chain.Tx{cb}
	child.MerkleRoot = child.ComputeMerkleRoot()

	grandchild := &chain.Block{
		Height:     2,
		PrevHash:   child.Hash(),
		Time:       child.Time + 1000,
		Difficulty: 1,
		Miner:      w.Address(),
	}
	cb2 := &chain.Tx{From: crypto.CoinbaseAddress, To: w.Address(), Amount: chain.TestParams().RewardAt(2), Nonce: 2, Time: grandchild.Time}
	grandchild.Txs = []*chain.Tx{cb2}
	grandchild.MerkleRoot = grandchild.ComputeMerkleRoot()

	if err := n.SubmitSolution(grandchild); err == nil {
		t.Fatal("orphan should fail with ErrOrphan")
	}
	if n.TipHeight() != 0 {
		t.Fatal("orphan should not change tip")
	}
	if err := n.SubmitSolution(child); err != nil {
		t.Fatal(err)
	}
	if n.TipHeight() != 2 {
		t.Fatalf("orphan promotion failed, height = %d", n.TipHeight())
	}
}

func TestStaleMempoolTx(t *testing.T) {
	n, w := newNode(t)
	w2, _ := crypto.GenerateWallet()
	w3, _ := crypto.GenerateWallet()

	s := state.New()
	s.Credit(w.Address(), 10e8)
	n.mu.Lock()
	n.state = s
	n.pendingDirty = true
	n.mu.Unlock()

	tx1 := &chain.Tx{From: w.Address(), To: w2.Address(), Amount: 1e8, Nonce: 0, Time: 1, PubKey: w.PubKey()}
	tx1.Sig = w.Sign(tx1.SigningBytes())
	if err := n.AddTx(tx1, ""); err != nil {
		t.Fatal(err)
	}

	tx2 := &chain.Tx{From: w.Address(), To: w3.Address(), Amount: 1e8, Nonce: 0, Time: 1, PubKey: w.PubKey()}
	tx2.Sig = w.Sign(tx2.SigningBytes())
	competitor := forkBlock(t, n, w, tipEntry(t, n), "C")
	competitor.Txs = append(competitor.Txs, tx2)
	competitor.MerkleRoot = competitor.ComputeMerkleRoot()
	if err := n.SubmitSolution(competitor); err != nil {
		t.Fatal(err)
	}

	if n.TipHash() != competitor.Hash() {
		t.Fatal("competitor block should be tip")
	}
	if n.MempoolSize() != 0 {
		t.Fatalf("stale conflicting tx should be dropped from mempool, mempool=%d", n.MempoolSize())
	}
	if got := n.GetAccount(w3.Address()).Balance; got != 1e8 {
		t.Fatalf("w3 balance = %d", got)
	}
}

func TestInvalidTxsRejected(t *testing.T) {
	n, w := newNode(t)
	w2, _ := crypto.GenerateWallet()
	bad := &chain.Tx{From: w.Address(), To: w2.Address(), Amount: 5e8, Nonce: 0, Time: 1, PubKey: w.PubKey(), Sig: []byte("garbage")}
	if err := n.AddTx(bad, ""); err == nil {
		t.Fatal("garbage signature accepted into mempool")
	}
}
