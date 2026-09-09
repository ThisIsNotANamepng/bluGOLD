package miner

import (
	"encoding/binary"
	"math/big"
	"math/bits"
)

// This file implements a small, self-contained SHA-256 block compressor and
// a "midstate" builder on top of it. It exists so a fast backend (GPU today,
// conceivably SIMD later) only has to hash the handful of bytes that change
// between nonce trials — the block height, prev-hash, timestamp, difficulty,
// miner address, extra-nonce and merkle root are identical for every trial
// within one mining round, so the SHA-256 state after compressing them can
// be computed once on the CPU and handed to the backend as a starting point.
//
// This intentionally duplicates a slice of crypto/sha256's algorithm rather
// than reaching into its internal state via encoding.BinaryMarshaler, so the
// stdlib's on-disk state format is never a dependency. TestMidstateMatchesHash
// checks the two paths produce identical hashes for real header shapes.

var sha256K = [64]uint32{
	0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
	0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
	0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
	0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
	0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
	0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
	0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
	0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
}

var sha256IV = [8]uint32{
	0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
	0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
}

// sha256Compress runs the standard SHA-256 compression function, updating h
// in place with one 64-byte block.
func sha256Compress(h *[8]uint32, block []byte) {
	var w [64]uint32
	for i := 0; i < 16; i++ {
		w[i] = binary.BigEndian.Uint32(block[i*4 : i*4+4])
	}
	for i := 16; i < 64; i++ {
		s0 := bits.RotateLeft32(w[i-15], -7) ^ bits.RotateLeft32(w[i-15], -18) ^ (w[i-15] >> 3)
		s1 := bits.RotateLeft32(w[i-2], -17) ^ bits.RotateLeft32(w[i-2], -19) ^ (w[i-2] >> 10)
		w[i] = w[i-16] + s0 + w[i-7] + s1
	}
	a, b, c, d, e, f, g, hh := h[0], h[1], h[2], h[3], h[4], h[5], h[6], h[7]
	for i := 0; i < 64; i++ {
		s1 := bits.RotateLeft32(e, -6) ^ bits.RotateLeft32(e, -11) ^ bits.RotateLeft32(e, -25)
		ch := (e & f) ^ (^e & g)
		t1 := hh + s1 + ch + sha256K[i] + w[i]
		s0 := bits.RotateLeft32(a, -2) ^ bits.RotateLeft32(a, -13) ^ bits.RotateLeft32(a, -22)
		maj := (a & b) ^ (a & c) ^ (b & c)
		t2 := s0 + maj
		hh, g, f, e, d, c, b, a = g, f, e, d+t1, c, b, a, t1+t2
	}
	h[0] += a
	h[1] += b
	h[2] += c
	h[3] += d
	h[4] += e
	h[5] += f
	h[6] += g
	h[7] += hh
}

// gpuMidstate is everything a fast backend needs to search nonces for one
// candidate block header: the SHA-256 state after all complete 64-byte
// blocks of the fixed prefix, and a template for the final (partial,
// padded) block with two words reserved for the nonce.
type gpuMidstate struct {
	H         [8]uint32
	Block     [16]uint32
	NonceWord int // index into Block of the nonce's low 32 bits; NonceWord+1 holds the high 32 bits
}

// buildMidstate computes the SHA-256 midstate for hdr, a block's
// HeaderBytes() (the trailing 8-byte Nonce field's value is irrelevant — it
// is overwritten per trial by the backend).
//
// ok is false when the header's fixed prefix doesn't leave room for the
// nonce and SHA-256's padding inside a single trailing block — never true
// for bluGOLD's current wire format (checked defensively so a future codec
// change fails safe onto the CPU path instead of mining garbage).
func buildMidstate(hdr []byte) (mid gpuMidstate, ok bool) {
	if len(hdr) < 8 {
		return mid, false
	}
	prefixLen := len(hdr) - 8
	full := prefixLen / 64
	rem := prefixLen % 64
	// Need room in the final block for: rem prefix bytes + 8 nonce bytes +
	// 1 marker byte + an 8-byte length field, and the nonce must land on a
	// 4-byte word boundary so two whole words hold it.
	if rem%4 != 0 || rem+8+1+8 > 64 {
		return mid, false
	}
	h := sha256IV
	for i := 0; i < full; i++ {
		sha256Compress(&h, hdr[i*64:i*64+64])
	}
	mid.H = h

	var block [64]byte
	copy(block[:rem], hdr[full*64:full*64+rem])
	// block[rem:rem+8] stays zero — the nonce, filled in per trial.
	block[rem+8] = 0x80
	binary.BigEndian.PutUint64(block[56:64], uint64(len(hdr))*8)
	for i := 0; i < 16; i++ {
		mid.Block[i] = binary.BigEndian.Uint32(block[i*4 : i*4+4])
	}
	mid.NonceWord = rem / 4
	return mid, true
}

// nonceWords returns the two big-endian 32-bit SHA-256 message-schedule
// words that HeaderBytes' little-endian 8-byte Nonce field turns into.
func nonceWords(nonce uint64) (lo, hi uint32) {
	return bits.ReverseBytes32(uint32(nonce)), bits.ReverseBytes32(uint32(nonce >> 32))
}

// finish completes the hash for a given nonce from a midstate. It is used
// by the OpenCL backend's Go-side self-test and by unit tests; production
// GPU search does the equivalent work in the kernel.
func (mid gpuMidstate) finish(nonce uint64) [32]byte {
	h := mid.H
	block := mid.Block
	lo, hi := nonceWords(nonce)
	block[mid.NonceWord] = lo
	block[mid.NonceWord+1] = hi
	var bb [64]byte
	for i := 0; i < 16; i++ {
		binary.BigEndian.PutUint32(bb[i*4:i*4+4], block[i])
	}
	sha256Compress(&h, bb[:])
	var out [32]byte
	for i := 0; i < 8; i++ {
		binary.BigEndian.PutUint32(out[i*4:i*4+4], h[i])
	}
	return out
}

// targetWords packs a difficulty target into 8 big-endian 32-bit words,
// matching the byte order of a SHA-256 digest so the two can be compared
// word-by-word most-significant-first.
func targetWords(t *big.Int) [8]uint32 {
	var buf [32]byte
	t.FillBytes(buf[:])
	var out [8]uint32
	for i := 0; i < 8; i++ {
		out[i] = binary.BigEndian.Uint32(buf[i*4 : i*4+4])
	}
	return out
}
