package state

import (
	"testing"

	"blugold/internal/chain"
	"blugold/internal/crypto"
)

func newFundedState(t *testing.T, addr chain.Address, amt uint64) *State {
	t.Helper()
	s := New()
	s.Credit(addr, amt)
	return s
}

func signTx(t *testing.T, w *crypto.Wallet, to crypto.Address, amount, fee, nonce uint64, tm int64) *chain.Tx {
	t.Helper()
	tx := &chain.Tx{
		From:   w.Address(),
		To:     to,
		Amount: amount,
		Fee:    fee,
		Nonce:  nonce,
		Time:   tm,
		PubKey: w.PubKey(),
	}
	tx.Sig = w.Sign(tx.SigningBytes())
	return tx
}

func TestApplyTx(t *testing.T) {
	w, _ := crypto.GenerateWallet()
	w2, _ := crypto.GenerateWallet()
	s := newFundedState(t, w.Address(), 10e8)

	tx := signTx(t, w, w2.Address(), 4e8, 1e8, 0, 1)
	if err := s.ValidateTx(tx); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := s.ApplyTx(tx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := s.Account(w.Address()); got.Balance != 5e8 || got.Nonce != 1 {
		t.Errorf("sender state = %+v", got)
	}
	if got := s.Account(w2.Address()).Balance; got != 4e8 {
		t.Errorf("recipient balance = %d", got)
	}
}

func TestBadNonce(t *testing.T) {
	w, _ := crypto.GenerateWallet()
	s := newFundedState(t, w.Address(), 10e8)
	tx := signTx(t, w, w.Address(), 1e8, 0, 5, 1)
	if err := s.ApplyTx(tx); err == nil {
		t.Fatal("future nonce accepted")
	}
	tx = signTx(t, w, w.Address(), 1e8, 0, 0, 1)
	if err := s.ApplyTx(tx); err != nil {
		t.Fatal(err)
	}
	replay := signTx(t, w, w.Address(), 1e8, 0, 0, 2)
	if err := s.ApplyTx(replay); err == nil {
		t.Fatal("replayed nonce accepted")
	}
}

func TestInsufficientFunds(t *testing.T) {
	w, _ := crypto.GenerateWallet()
	w2, _ := crypto.GenerateWallet()
	s := newFundedState(t, w.Address(), 1e8)
	tx := signTx(t, w, w2.Address(), 1e8, 1, 0, 1)
	if err := s.ApplyTx(tx); err == nil {
		t.Fatal("spend exceeding balance accepted")
	}
}

func TestBadSignature(t *testing.T) {
	w, _ := crypto.GenerateWallet()
	w2, _ := crypto.GenerateWallet()
	s := newFundedState(t, w.Address(), 10e8)
	tx := signTx(t, w, w2.Address(), 1e8, 0, 0, 1)
	tx.Amount = 9e8
	if err := s.ValidateTx(tx); err == nil {
		t.Fatal("forged amount accepted")
	}
}

func TestApplyBlock(t *testing.T) {
	w, _ := crypto.GenerateWallet()
	w2, _ := crypto.GenerateWallet()
	p := chain.TestParams()
	s := newFundedState(t, w.Address(), 10e8)

	cb := &chain.Tx{From: crypto.CoinbaseAddress, To: w2.Address(), Amount: p.RewardAt(1) + 1e8, Time: 1}
	b := &chain.Block{Height: 1, Time: 2, Miner: w2.Address(), Txs: []*chain.Tx{cb, signTx(t, w, w2.Address(), 1e8, 1e8, 0, 1)}}
	if err := s.ApplyBlock(b, p); err != nil {
		t.Fatalf("apply block: %v", err)
	}
	if got := s.Account(w.Address()); got.Balance != 8e8 {
		t.Errorf("sender after block = %+v", got)
	}
	if got := s.Account(w2.Address()).Balance; got != p.RewardAt(1)+1e8+1e8 {
		t.Errorf("miner+recipient balance = %d", got)
	}
}

func TestApplyBlockWrongCoinbase(t *testing.T) {
	w, _ := crypto.GenerateWallet()
	w2, _ := crypto.GenerateWallet()
	p := chain.TestParams()
	s := newFundedState(t, w.Address(), 10e8)

	cb := &chain.Tx{From: crypto.CoinbaseAddress, To: w2.Address(), Amount: p.RewardAt(1) + 1, Time: 1}
	b := &chain.Block{Height: 1, Time: 2, Miner: w2.Address(), Txs: []*chain.Tx{cb}}
	if err := s.ApplyBlock(b, p); err == nil {
		t.Fatal("inflated coinbase accepted")
	}
}

func TestMinerCannotSpendSameBlockReward(t *testing.T) {
	w, _ := crypto.GenerateWallet()
	w2, _ := crypto.GenerateWallet()
	p := chain.TestParams()
	s := New()

	coin := p.RewardAt(1)
	cb := &chain.Tx{From: crypto.CoinbaseAddress, To: w.Address(), Amount: coin, Time: 1}
	spend := signTx(t, w, w2.Address(), coin, 0, 0, 1)
	b := &chain.Block{Height: 1, Time: 2, Miner: w.Address(), Txs: []*chain.Tx{cb, spend}}
	if err := s.ApplyBlock(b, p); err == nil {
		t.Fatal("same-block coinbase spend accepted")
	}
}

func TestCloneIndependence(t *testing.T) {
	w, _ := crypto.GenerateWallet()
	s := newFundedState(t, w.Address(), 5e8)
	c := s.Clone()
	c.Credit(w.Address(), 5e8)
	if s.Account(w.Address()).Balance != 5e8 {
		t.Fatal("clone mutated original")
	}
}
