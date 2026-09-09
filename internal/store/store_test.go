package store

import (
	"testing"

	"blugold/internal/chain"
	"blugold/internal/crypto"
)

func TestBlockPersistence(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	w, _ := crypto.GenerateWallet()
	cb := &chain.Tx{From: crypto.CoinbaseAddress, To: w.Address(), Amount: 50e8, Nonce: 1, Time: 5}
	b := &chain.Block{Height: 1, Time: 6, Difficulty: 1, Miner: w.Address(), Txs: []*chain.Tx{cb}}
	b.MerkleRoot = b.ComputeMerkleRoot()

	if err := st.AppendBlock(b); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	blocks, err := st2.LoadBlocks()
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Hash() != b.Hash() {
		t.Fatalf("roundtrip mismatch: %+v", blocks)
	}
}

func TestLoadEmpty(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	blocks, err := st.LoadBlocks()
	if err != nil || len(blocks) != 0 {
		t.Fatalf("empty store: %v %v", blocks, err)
	}
}

func TestPeersPersistence(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	addrs := []string{"10.0.0.1:7007", "10.0.0.2:7007"}
	if err := st.SavePeers(addrs); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadPeers()
	if err != nil || len(got) != 2 || got[0] != addrs[0] {
		t.Fatalf("peers roundtrip: %v %v", got, err)
	}
}
