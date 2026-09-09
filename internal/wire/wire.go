// Package wire defines the p2p message envelope and length-prefixed JSON framing.
package wire

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"blugold/internal/chain"
)

const (
	ProtocolVersion = 2
	MaxFrameSize    = 8 << 20
)

const (
	MsgVersion   = "version"
	MsgPeers     = "peers"
	MsgGetBlocks = "getblocks"
	MsgBlocks    = "blocks"
	MsgNewTx     = "newtx"
	MsgNewBlock  = "newblock"
)

type Envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func NewEnvelope(msgType string, payload any) (*Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Envelope{Type: msgType, Payload: raw}, nil
}

type VersionMsg struct {
	Protocol   int    `json:"protocol"`
	ListenAddr string `json:"listen_addr"`
	Height     uint64 `json:"height"`
}

type PeersMsg struct {
	Addrs []string `json:"addrs"`
}

// GetBlocksMsg asks a peer to continue our chain. Locator is a list of block
// hashes on the requester's best branch, newest first, with exponentially
// growing gaps and genesis last. The responder answers with the blocks that
// follow the newest locator entry present on its own active chain, so two
// nodes find their common ancestor even when their chains diverged long ago.
// Requesting by height alone cannot do that: the answer would be a run of
// blocks whose parents the requester has never seen.
type GetBlocksMsg struct {
	Locator []chain.Hash `json:"locator"`
	Count   int          `json:"count"`
}

type BlocksMsg struct {
	Blocks []*chain.Block `json:"blocks"`
}

type NewTxMsg struct {
	Tx *chain.Tx `json:"tx"`
}

type NewBlockMsg struct {
	Block *chain.Block `json:"block"`
}

func WriteFrame(w io.Writer, env *Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if len(data) > MaxFrameSize {
		return fmt.Errorf("frame too large: %d", len(data))
	}
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(data)))
	if _, err := w.Write(l[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func ReadFrame(r io.Reader) (*Envelope, error) {
	var l [4]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(l[:])
	if n > MaxFrameSize {
		return nil, fmt.Errorf("frame too large: %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(buf, &env); err != nil {
		return nil, err
	}
	if env.Type == "" {
		return nil, errors.New("empty message type")
	}
	return &env, nil
}
