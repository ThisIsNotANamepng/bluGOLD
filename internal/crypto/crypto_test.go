package crypto

import (
	"crypto/ed25519"
	"path/filepath"
	"testing"
)

func TestAddressRoundTrip(t *testing.T) {
	w, err := GenerateWallet()
	if err != nil {
		t.Fatal(err)
	}
	addr := w.Address()
	if !addr.Valid() {
		t.Fatalf("generated address not valid: %q", addr)
	}
	if addr != AddressFromPubKey(w.PubKey()) {
		t.Fatal("address not derived deterministically")
	}
	if len(addr) != addrLength {
		t.Fatalf("unexpected address length %d: %q", len(addr), addr)
	}
}

func TestAddressTamper(t *testing.T) {
	w, _ := GenerateWallet()
	a := string(w.Address())
	corrupt := []byte(a)
	if corrupt[10] == 'a' {
		corrupt[10] = 'b'
	} else {
		corrupt[10] = 'a'
	}
	if Address(corrupt).Valid() {
		t.Fatal("corrupted address should not validate")
	}
}

func TestSignVerify(t *testing.T) {
	w, _ := GenerateWallet()
	msg := []byte("hello club")
	sig := w.Sign(msg)
	if !Verify(w.PubKey(), msg, sig) {
		t.Fatal("valid signature rejected")
	}
	bad := append([]byte{}, msg...)
	bad[0] ^= 1
	if Verify(w.PubKey(), bad, sig) {
		t.Fatal("tampered message accepted")
	}
	w2, _ := GenerateWallet()
	if Verify(w2.PubKey(), msg, sig) {
		t.Fatal("wrong pubkey accepted signature")
	}
}

func TestWalletSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wallet.json")
	w, err := GenerateWallet()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveWallet(path, w); err != nil {
		t.Fatal(err)
	}
	lw, err := LoadWallet(path)
	if err != nil {
		t.Fatal(err)
	}
	if lw.Address() != w.Address() {
		t.Fatal("wallet roundtrip changed keys")
	}
	if lw.Priv[0] != w.Priv[0] {
		t.Fatal("private key mismatch")
	}
}

func TestCoinbaseAddressInvalid(t *testing.T) {
	if CoinbaseAddress.Valid() {
		t.Fatal("empty address must not validate")
	}
}

func TestPubKeySize(t *testing.T) {
	w, _ := GenerateWallet()
	if len(w.PubKey()) != ed25519.PublicKeySize {
		t.Fatal("bad pubkey size")
	}
}
