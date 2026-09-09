package chain

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"

	"blugold/internal/crypto"
)

type Block struct {
	Height     uint64         `json:"height"`
	PrevHash   Hash           `json:"prev"`
	Time       int64          `json:"time"`
	Difficulty uint64         `json:"difficulty"`
	Miner      crypto.Address `json:"miner"`
	ExtraNonce uint64         `json:"extra_nonce"`
	MerkleRoot Hash           `json:"merkle"`
	Nonce      uint64         `json:"nonce"`
	Txs        []*Tx          `json:"txs"`
}

func (b *Block) HeaderBytes() []byte {
	buf := bytes.NewBuffer(nil)
	writeU64(buf, b.Height)
	writeBytes(buf, b.PrevHash[:])
	writeI64(buf, b.Time)
	writeU64(buf, b.Difficulty)
	writeString(buf, string(b.Miner))
	writeU64(buf, b.ExtraNonce)
	writeBytes(buf, b.MerkleRoot[:])
	writeU64(buf, b.Nonce)
	return buf.Bytes()
}

func (b *Block) Hash() Hash { return HashBytes(b.HeaderBytes()) }

// MeetsTarget reports whether the block hash satisfies its own difficulty target.
func (b *Block) MeetsTarget(p Params) bool {
	if b.Difficulty == 0 {
		return false
	}
	h := b.Hash()
	hv := new(big.Int).SetBytes(h[:])
	return hv.Cmp(p.Target(b.Difficulty)) < 0
}

// ValidateBasic checks structure against the parent block. Difficulty/PoW are checked separately.
func (b *Block) ValidateBasic(p Params, parent *Block, now int64) error {
	if parent == nil {
		return errors.New("no parent")
	}
	if b.Height != parent.Height+1 {
		return fmt.Errorf("height %d does not follow %d", b.Height, parent.Height)
	}
	if b.PrevHash != parent.Hash() {
		return errors.New("prev hash mismatch")
	}
	if b.Time <= parent.Time {
		return fmt.Errorf("timestamp %d not after parent %d", b.Time, parent.Time)
	}
	if b.Time > now+p.MaxFutureTime {
		return fmt.Errorf("timestamp too far in future")
	}
	if len(b.Txs) == 0 || !b.Txs[0].IsCoinbase() {
		return errors.New("first tx must be coinbase")
	}
	if len(b.Txs) > p.MaxTxsPerBlock {
		return fmt.Errorf("too many txs: %d", len(b.Txs))
	}
	for i, tx := range b.Txs[1:] {
		if tx.IsCoinbase() {
			return fmt.Errorf("extra coinbase at index %d", i+1)
		}
	}
	if b.Miner == crypto.CoinbaseAddress || !b.Miner.Valid() {
		return errors.New("invalid miner address")
	}
	if root := b.ComputeMerkleRoot(); root != b.MerkleRoot {
		return errors.New("merkle root mismatch")
	}
	return nil
}

func (b *Block) ComputeMerkleRoot() Hash { return MerkleRoot(b.Txs) }

func (b *Block) ValidatePoW(p Params) error {
	if b.Difficulty == 0 {
		return errors.New("zero difficulty")
	}
	if !b.MeetsTarget(p) {
		return errors.New("hash does not meet target")
	}
	return nil
}

// MerkleRoot computes the pairwise-sha256 merkle root over tx hashes, duplicating the last on odd levels.
func MerkleRoot(txs []*Tx) Hash {
	if len(txs) == 0 {
		return Hash{}
	}
	level := make([]Hash, len(txs))
	for i, tx := range txs {
		level[i] = tx.Hash()
	}
	for len(level) > 1 {
		if len(level)%2 == 1 {
			level = append(level, level[len(level)-1])
		}
		next := make([]Hash, 0, len(level)/2)
		for i := 0; i < len(level); i += 2 {
			cat := make([]byte, 0, 64)
			cat = append(cat, level[i][:]...)
			cat = append(cat, level[i+1][:]...)
			next = append(next, sha256.Sum256(cat))
		}
		level = next
	}
	return level[0]
}

func Genesis(p Params) *Block {
	return &Block{
		Height:     0,
		Time:       p.GenesisTime,
		Difficulty: 1,
		Miner:      crypto.CoinbaseAddress,
		MerkleRoot: MerkleRoot(nil),
	}
}
