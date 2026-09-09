package wire

import (
	"bytes"
	"encoding/json"
	"testing"

	"blugold/internal/chain"
	"blugold/internal/crypto"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	env, err := NewEnvelope(MsgGetBlocks, GetBlocksMsg{From: 5, Count: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&buf, env); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != MsgGetBlocks {
		t.Fatalf("type = %s", got.Type)
	}
	var m GetBlocksMsg
	if err := json.Unmarshal(got.Payload, &m); err != nil {
		t.Fatal(err)
	}
	if m.From != 5 || m.Count != 10 {
		t.Errorf("payload = %+v", m)
	}
}

func TestFrameBlockRoundTrip(t *testing.T) {
	w, _ := crypto.GenerateWallet()
	cb := &chain.Tx{From: crypto.CoinbaseAddress, To: w.Address(), Amount: 50e8, Nonce: 1, Time: 5}
	b := &chain.Block{Height: 1, Time: 6, Difficulty: 1, Miner: w.Address(), Txs: []*chain.Tx{cb}}
	b.MerkleRoot = b.ComputeMerkleRoot()

	var buf bytes.Buffer
	env, _ := NewEnvelope(MsgNewBlock, NewBlockMsg{Block: b})
	if err := WriteFrame(&buf, env); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	var m NewBlockMsg
	if err := json.Unmarshal(got.Payload, &m); err != nil {
		t.Fatal(err)
	}
	if m.Block.Hash() != b.Hash() {
		t.Fatalf("block hash changed across frame: %s vs %s", m.Block.Hash(), b.Hash())
	}
	if m.Block.Txs[0].To != w.Address() {
		t.Fatal("tx fields lost")
	}
}

func TestFrameTooLarge(t *testing.T) {
	big := bytes.Repeat([]byte("a"), MaxFrameSize+10)
	var buf bytes.Buffer
	if err := WriteFrame(&buf, &Envelope{Type: "x", Payload: big}); err == nil {
		t.Fatal("oversize frame accepted")
	}
}
