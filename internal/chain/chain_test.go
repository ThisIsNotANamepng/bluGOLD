package chain

import (
	"testing"

	"blugold/internal/crypto"
)

func TestFormatParseAmount(t *testing.T) {
	cases := []struct {
		in  uint64
		str string
	}{
		{0, "0.00000000"},
		{1, "0.00000001"},
		{1e8, "1.00000000"},
		{15e8, "15.00000000"},
		{123456789, "1.23456789"},
	}
	for _, c := range cases {
		if got := FormatAmount(c.in); got != c.str {
			t.Errorf("FormatAmount(%d) = %q, want %q", c.in, got, c.str)
		}
		back, err := ParseAmount(c.str)
		if err != nil || back != c.in {
			t.Errorf("ParseAmount(%q) = %d, %v; want %d", c.str, back, err, c.in)
		}
	}
	for _, bad := range []string{"-1", "1.123456789", "abc", "", "1e5", "1.2.3"} {
		if _, err := ParseAmount(bad); err == nil {
			t.Errorf("ParseAmount(%q) should fail", bad)
		}
	}
	if _, err := ParseAmount("184467440737.09551615"); err != nil {
		t.Errorf("max uint64 amount rejected: %v", err)
	}
	if _, err := ParseAmount("184467440737.09551616"); err == nil {
		t.Error("overflowing fraction accepted")
	}
	if _, err := ParseAmount("184467440738"); err == nil {
		t.Error("overflowing whole amount accepted")
	}
}

func TestRewardHalving(t *testing.T) {
	p := DefaultParams()
	if got := p.RewardAt(0); got != 1e8 {
		t.Errorf("reward at 0 = %d", got)
	}
	if got := p.RewardAt(10080); got != 5e7 {
		t.Errorf("reward at first halving = %d", got)
	}
	if got := p.RewardAt(10080 * 2); got != 25e6 {
		t.Errorf("reward at second halving = %d", got)
	}
	if got := p.RewardAt(10080 * 27); got != 0 {
		t.Errorf("reward deep in halvings should be 0, got %d", got)
	}
}

func TestTotalSupply(t *testing.T) {
	p := DefaultParams()
	var total uint64
	for h := uint64(0); ; h++ {
		r := p.RewardAt(h)
		if r == 0 {
			break
		}
		sum := total + r
		if sum < total {
			t.Fatal("supply overflow")
		}
		total = sum
	}
	if want := uint64(2015999879040); total != want {
		t.Fatalf("total supply = %d bluglets, want %d", total, want)
	}
}

func TestNextDifficultyClamp(t *testing.T) {
	p := Params{TargetBlockTime: 1000, BlocksPerRetarget: 4, HalvingInterval: 100, InitialReward: 1, MaxFutureTime: 1e9}
	tip := &Block{Height: 3, Difficulty: 100}
	anchor := &Block{Height: 0, Time: 0}

	tip.Time = 4 * 1000
	if got := NextDifficulty(p, tip, anchor); got != 100 {
		t.Errorf("on-target retarget should hold: %d", got)
	}
	tip.Time = 100
	if got := NextDifficulty(p, tip, anchor); got != 400 {
		t.Errorf("too-fast retarget should multiply by 4: %d", got)
	}
	tip.Time = 40000
	if got := NextDifficulty(p, tip, anchor); got != 25 {
		t.Errorf("too-slow retarget should divide by 4: %d", got)
	}
	tip.Time = 3000
	got := NextDifficulty(p, tip, anchor)
	want := uint64(100) * 4000 / 3000
	if got != want {
		t.Errorf("in-range retarget = %d, want %d", got, want)
	}
	if NextDifficulty(p, tip, nil) != 100 {
		t.Error("missing anchor should keep difficulty")
	}
}

func TestNextDifficultyMinOne(t *testing.T) {
	p := Params{TargetBlockTime: 1000, BlocksPerRetarget: 1, HalvingInterval: 100, InitialReward: 1, MaxFutureTime: 1e9}
	tip := &Block{Height: 0, Difficulty: 2, Time: 9000}
	anchor := &Block{Height: 0, Time: 0}
	if got := NextDifficulty(p, tip, anchor); got != 1 {
		t.Errorf("difficulty should floor at 1, got %d", got)
	}
}

func TestMerkleRoot(t *testing.T) {
	if MerkleRoot(nil).IsZero() != true {
		t.Error("empty merkle should be zero hash")
	}
	txs := []*Tx{{To: "x", Amount: 1}, {To: "y", Amount: 2}, {To: "z", Amount: 3}}
	root3 := MerkleRoot(txs)
	if root3.IsZero() {
		t.Fatal("root empty")
	}
	// duplicating last element must not change root for 3 txs
	root4 := MerkleRoot(append(txs, txs[2]))
	if root4 != root3 {
		t.Error("odd-length merkle duplication broken")
	}
	// changing a tx changes the root
	txs[1].Amount = 9
	if MerkleRoot(txs) == root3 {
		t.Error("root should change when tx changes")
	}
}

func TestBlockHashDeterministic(t *testing.T) {
	b := &Block{Height: 1, Time: 1234, Difficulty: 3, Miner: "blu1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MerkleRoot: Hash{}}
	h1 := b.Hash()
	b.Nonce++
	if b.Hash() == h1 {
		t.Error("hash should change with nonce")
	}
	b.Nonce--
	if b.Hash() != h1 {
		t.Error("hash not deterministic")
	}
}

func TestPowTarget(t *testing.T) {
	p := TestParams()
	b := &Block{Height: 1, Difficulty: 1}
	if !b.MeetsTarget(p) {
		t.Error("difficulty 1 must accept any hash")
	}
	b.Difficulty = 1 << 20
	// find a nonce meeting difficulty 2^20
	passed := false
	for n := uint64(0); n < 5_000_000; n++ {
		b.Nonce = n
		if b.MeetsTarget(p) {
			passed = true
			break
		}
	}
	if !passed {
		t.Fatal("no nonce met target")
	}
	if err := b.ValidatePoW(p); err != nil {
		t.Fatal(err)
	}
	b.Difficulty = 1 << 40
	if b.ValidatePoW(p) == nil {
		t.Fatal("same hash should fail higher difficulty")
	}
}

func TestValidateBasic(t *testing.T) {
	p := TestParams()
	parent := Genesis(p)
	now := parent.Time + 5000

	b := &Block{Height: 1, PrevHash: parent.Hash(), Time: now + 1, Difficulty: 1, Miner: validTestAddr(t),
		Txs: []*Tx{{From: crypto.CoinbaseAddress, To: b0Miner(t), Amount: 50e8, Time: now + 1}}}
	b.MerkleRoot = b.ComputeMerkleRoot()
	if err := b.ValidateBasic(p, parent, now); err != nil {
		t.Fatalf("valid block rejected: %v", err)
	}

	bad := *b
	bad.PrevHash = Hash{1}
	if err := (&bad).ValidateBasic(p, parent, now); err == nil {
		t.Error("prev hash mismatch accepted")
	}

	bad = *b
	bad.Time = parent.Time
	if err := (&bad).ValidateBasic(p, parent, now); err == nil {
		t.Error("non-increasing time accepted")
	}

	bad = *b
	bad.Time = now + p.MaxFutureTime + 1
	if err := (&bad).ValidateBasic(p, parent, now); err == nil {
		t.Error("future time accepted")
	}

	bad = *b
	bad.Txs = nil
	if err := (&bad).ValidateBasic(p, parent, now); err == nil {
		t.Error("missing coinbase accepted")
	}

	bad = *b
	bad.Miner = ""
	if err := (&bad).ValidateBasic(p, parent, now); err == nil {
		t.Error("empty miner accepted")
	}
}

func validTestAddr(t *testing.T) crypto.Address {
	t.Helper()
	w, err := crypto.GenerateWallet()
	if err != nil {
		t.Fatal(err)
	}
	return w.Address()
}

func b0Miner(t *testing.T) crypto.Address { return validTestAddr(t) }

func TestTxVerifyAndHash(t *testing.T) {
	w, _ := crypto.GenerateWallet()
	w2, _ := crypto.GenerateWallet()

	tx := &Tx{
		From:   w.Address(),
		To:     w2.Address(),
		Amount: 1e8,
		Nonce:  0,
		Time:   123456,
		PubKey: w.PubKey(),
	}
	tx.Sig = w.Sign(tx.SigningBytes())
	if err := tx.Verify(); err != nil {
		t.Fatalf("valid tx rejected: %v", err)
	}
	h := tx.Hash()

	tx2 := *tx
	tx2.Amount = 2e8
	tx2.Sig = tx.Sig
	if err := tx2.Verify(); err == nil {
		t.Error("tampered amount accepted")
	}
	if tx.Hash() != h {
		t.Error("hash changed without mutation")
	}

	cb := &Tx{From: crypto.CoinbaseAddress, To: w2.Address(), Amount: 50e8, Nonce: 1, Time: 1}
	if !cb.IsCoinbase() {
		t.Fatal("coinbase misdetected")
	}
	if err := cb.Verify(); err != nil {
		t.Fatalf("valid coinbase rejected: %v", err)
	}
	cbBad := *cb
	cbBad.Sig = []byte("notarealsignature")
	if err := cbBad.Verify(); err == nil {
		t.Error("coinbase with signature must be rejected")
	}
}

func TestGenesis(t *testing.T) {
	g := Genesis(TestParams())
	if g.Height != 0 || g.Difficulty != 1 || len(g.Txs) != 0 {
		t.Fatal("bad genesis")
	}
	if !g.MeetsTarget(TestParams()) {
		t.Fatal("genesis must pass PoW")
	}
}
