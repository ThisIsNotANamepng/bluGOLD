// Package crypto provides bluGOLD keypairs, wallet files and addresses.
package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Address is a bluGOLD address: "blu1" + base32(sha256(pubkey)[0:20]) + 4 hex checksum chars.
type Address string

const (
	AddressPrefix = "blu1"
	addrBodyBytes = 20
	addrBodyChars = 32
	checksumLen   = 4
	addrLength    = len(AddressPrefix) + addrBodyChars + checksumLen
)

// CoinbaseAddress is the empty From address reserved for coinbase transactions.
const CoinbaseAddress Address = ""

var b32Encoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func computeChecksum(body string) string {
	h := sha256.Sum256([]byte(AddressPrefix + body))
	return hex.EncodeToString(h[:])[:checksumLen]
}

func AddressFromPubKey(pk ed25519.PublicKey) Address {
	if len(pk) != ed25519.PublicKeySize {
		return ""
	}
	sum := sha256.Sum256(pk)
	body := b32Encoding.EncodeToString(sum[:addrBodyBytes])
	return Address(AddressPrefix + body + computeChecksum(body))
}

func (a Address) Valid() bool {
	s := string(a)
	if len(s) != addrLength || !strings.HasPrefix(s, AddressPrefix) {
		return false
	}
	body := s[len(AddressPrefix) : len(s)-checksumLen]
	for _, r := range body {
		if !((r >= 'a' && r <= 'z') || (r >= '2' && r <= '7')) {
			return false
		}
	}
	return computeChecksum(body) == s[len(s)-checksumLen:]
}

// Wallet holds an ed25519 keypair. Priv marshals as base64 via encoding/json.
type Wallet struct {
	Priv ed25519.PrivateKey `json:"priv"`
}

func GenerateWallet() (*Wallet, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Wallet{Priv: priv}, nil
}

func (w *Wallet) PubKey() ed25519.PublicKey {
	return w.Priv.Public().(ed25519.PublicKey)
}

func (w *Wallet) Address() Address {
	return AddressFromPubKey(w.PubKey())
}

func (w *Wallet) Sign(msg []byte) []byte {
	return ed25519.Sign(w.Priv, msg)
}

func Verify(pk ed25519.PublicKey, msg, sig []byte) bool {
	if len(pk) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pk, msg, sig)
}

func WalletPath(dataDir string) string {
	return filepath.Join(dataDir, "wallet.json")
}

func SaveWallet(path string, w *Wallet) error {
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func LoadWallet(path string) (*Wallet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var w Wallet
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, err
	}
	if len(w.Priv) != ed25519.PrivateKeySize {
		return nil, errors.New("wallet file corrupt: bad key size")
	}
	return &w, nil
}
