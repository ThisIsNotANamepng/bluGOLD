package chain

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"

	"blugold/internal/crypto"
)

type Address = crypto.Address

type Tx struct {
	From   crypto.Address `json:"from"`
	To     crypto.Address `json:"to"`
	Amount uint64         `json:"amount"`
	Fee    uint64         `json:"fee"`
	Nonce  uint64         `json:"nonce"`
	Time   int64          `json:"time"`
	PubKey []byte         `json:"pub,omitempty"`
	Sig    []byte         `json:"sig,omitempty"`
}

// encode serializes the tx deterministically. signing excludes Sig, full includes it.
func (t *Tx) encode(signing bool) []byte {
	buf := bytes.NewBuffer(nil)
	writeString(buf, string(t.From))
	writeString(buf, string(t.To))
	writeU64(buf, t.Amount)
	writeU64(buf, t.Fee)
	writeU64(buf, t.Nonce)
	writeI64(buf, t.Time)
	writeBytes(buf, t.PubKey)
	if !signing {
		writeBytes(buf, t.Sig)
	}
	return buf.Bytes()
}

func (t *Tx) SigningBytes() []byte { return t.encode(true) }

func (t *Tx) Hash() Hash { return HashBytes(t.encode(false)) }

func (t *Tx) IsCoinbase() bool { return t.From == crypto.CoinbaseAddress }

// Verify checks intrinsic validity: structure, addresses, and signature.
func (t *Tx) Verify() error {
	if t.IsCoinbase() {
		if t.PubKey != nil || t.Sig != nil {
			return errors.New("coinbase must not carry pubkey or signature")
		}
		if t.Fee != 0 {
			return errors.New("coinbase fee must be zero")
		}
		if t.To == crypto.CoinbaseAddress || !t.To.Valid() {
			return errors.New("invalid coinbase output address")
		}
		if t.Amount == 0 {
			return errors.New("empty coinbase")
		}
		return nil
	}
	if !t.From.Valid() {
		return errors.New("invalid from address")
	}
	if !t.To.Valid() {
		return errors.New("invalid to address")
	}
	if t.Amount == 0 && t.Fee == 0 {
		return errors.New("zero-amount zero-fee tx")
	}
	if len(t.PubKey) != ed25519.PublicKeySize {
		return errors.New("bad pubkey length")
	}
	if crypto.AddressFromPubKey(t.PubKey) != t.From {
		return fmt.Errorf("pubkey does not match from address")
	}
	if !crypto.Verify(t.PubKey, t.SigningBytes(), t.Sig) {
		return errors.New("bad signature")
	}
	return nil
}
