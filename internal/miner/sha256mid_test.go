package miner

import (
	"math/big"
	"math/rand"
	"testing"

	"blugold/internal/chain"
	"blugold/internal/crypto"
)

// TestMidstateMatchesHash checks that buildMidstate + finish reproduces
// exactly what Block.Hash() computes, for header shapes the chain actually
// produces. This is what makes it safe for the GPU backend to resume
// hashing from a precomputed midstate instead of hashing the whole header
// per trial.
func TestMidstateMatchesHash(t *testing.T) {
	w, err := crypto.GenerateWallet()
	if err != nil {
		t.Fatal(err)
	}
	addr := w.Address()

	blocks := []*chain.Block{
		{Height: 0, Time: 1, Difficulty: 1, Miner: addr},
		{Height: 12345, PrevHash: chain.HashBytes([]byte("prev")), Time: 987654321, Difficulty: 42, Miner: addr, ExtraNonce: 7, MerkleRoot: chain.HashBytes([]byte("merkle"))},
		{Height: 1 << 40, PrevHash: chain.HashBytes([]byte("x")), Time: -1, Difficulty: ^uint64(0), Miner: addr, ExtraNonce: ^uint64(0), MerkleRoot: chain.HashBytes([]byte("y")), Nonce: 999},
	}

	for bi, b := range blocks {
		hdr := b.HeaderBytes()
		mid, ok := buildMidstate(hdr)
		if !ok {
			t.Fatalf("block %d: buildMidstate rejected a real header shape", bi)
		}
		for _, nonce := range []uint64{0, 1, 42, 1 << 32, ^uint64(0), uint64(rand.Int63())} {
			b.Nonce = nonce
			want := b.Hash()
			got := mid.finish(nonce)
			if [32]byte(want) != got {
				t.Fatalf("block %d nonce %d: midstate hash mismatch\n want %x\n got  %x", bi, nonce, want, got)
			}
		}
	}
}

// TestTargetWordsOrdering checks targetWords matches the big-endian,
// most-significant-word-first comparison the GPU kernel performs.
func TestTargetWordsOrdering(t *testing.T) {
	p := chain.DefaultParams()
	for _, diff := range []uint64{1, 2, 1000, 1 << 32} {
		target := p.Target(diff)
		words := targetWords(target)
		var buf [32]byte
		for i, w := range words {
			buf[i*4] = byte(w >> 24)
			buf[i*4+1] = byte(w >> 16)
			buf[i*4+2] = byte(w >> 8)
			buf[i*4+3] = byte(w)
		}
		got := new(big.Int).SetBytes(buf[:])
		if got.Cmp(target) != 0 {
			t.Fatalf("difficulty %d: targetWords round-trip mismatch: want %s got %s", diff, target, got)
		}
	}
}

// TestBuildMidstateRejectsShortHeader is a defensive check: a header too
// short to contain a nonce field must be rejected, not panic.
func TestBuildMidstateRejectsShortHeader(t *testing.T) {
	if _, ok := buildMidstate(nil); ok {
		t.Fatal("expected buildMidstate(nil) to report not-ok")
	}
	if _, ok := buildMidstate(make([]byte, 4)); ok {
		t.Fatal("expected buildMidstate of a too-short header to report not-ok")
	}
}
