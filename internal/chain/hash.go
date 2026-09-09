package chain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Hash is a 32-byte sha256 digest with hex JSON encoding.
type Hash [32]byte

func (h Hash) String() string { return hex.EncodeToString(h[:]) }

func (h Hash) Short() string { return h.String()[:12] }

func (h Hash) IsZero() bool { return h == Hash{} }

func (h Hash) MarshalJSON() ([]byte, error) {
	return json.Marshal(h.String())
}

func (h *Hash) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return fmt.Errorf("invalid hash %q", s)
	}
	copy(h[:], b)
	return nil
}

func HashBytes(b []byte) Hash { return sha256.Sum256(b) }
