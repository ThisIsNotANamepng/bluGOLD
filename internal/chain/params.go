package chain

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Params defines the consensus rules of a bluGOLD network. All times are unix milliseconds.
type Params struct {
	GenesisTime       int64  // unix ms of genesis block
	InitialReward     uint64 // bluglets per block in the first era
	HalvingInterval   uint64 // blocks per reward halving
	BlocksPerRetarget uint64 // blocks between difficulty retargets
	TargetBlockTime   int64  // target interval in ms
	MaxTxsPerBlock    int
	MaxFutureTime     int64 // how far ahead a block timestamp may be, in ms
}

func DefaultParams() Params {
	return Params{
		GenesisTime:       1727049600000,
		InitialReward:     1 * 1e8,
		HalvingInterval:   10080,
		BlocksPerRetarget: 20,
		TargetBlockTime:   360000,
		MaxTxsPerBlock:    1000,
		MaxFutureTime:     300000,
	}
}

// TestParams are stable for tests: difficulty never retargets within the first 1000 blocks.
func TestParams() Params {
	return Params{
		GenesisTime:       1700000000000,
		InitialReward:     1 * 1e8,
		HalvingInterval:   10080,
		BlocksPerRetarget: 1000,
		TargetBlockTime:   1000,
		MaxTxsPerBlock:    1000,
		MaxFutureTime:     300000,
	}
}

// RewardAt returns the block reward in bluglets at the given height.
func (p Params) RewardAt(height uint64) uint64 {
	halvings := height / p.HalvingInterval
	if halvings >= 64 {
		return 0
	}
	return p.InitialReward >> halvings
}

var MaxTarget = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

// Target converts a difficulty into the max PoW hash value allowed.
func (p Params) Target(difficulty uint64) *big.Int {
	if difficulty == 0 {
		difficulty = 1
	}
	return new(big.Int).Div(MaxTarget, new(big.Int).SetUint64(difficulty))
}

// NextDifficulty computes the difficulty required for the block after tip.
// anchor must be the block on tip's chain at height (tip.Height+1)-BlocksPerRetarget,
// or nil when the retarget interval has not completed.
func NextDifficulty(p Params, tip *Block, anchor *Block) uint64 {
	if tip == nil {
		return 1
	}
	if p.BlocksPerRetarget == 0 || anchor == nil || (tip.Height+1) < p.BlocksPerRetarget || (tip.Height+1)%p.BlocksPerRetarget != 0 {
		return tip.Difficulty
	}
	expected := p.TargetBlockTime * int64(p.BlocksPerRetarget)
	if expected <= 0 {
		expected = 1
	}
	actual := tip.Time - anchor.Time
	d := new(big.Int).SetUint64(tip.Difficulty)
	four := big.NewInt(4)
	switch {
	case actual <= 0 || actual*4 < expected:
		d.Mul(d, four)
	case actual > 4*expected:
		d.Div(d, four)
	default:
		d.Mul(d, big.NewInt(expected))
		d.Div(d, big.NewInt(actual))
	}
	if d.Sign() <= 0 {
		d.SetInt64(1)
	}
	if d.BitLen() > 64 {
		return math.MaxUint64
	}
	return d.Uint64()
}

const Bluglets = uint64(1e8)

// FormatAmount renders bluglets as a decimal BLG string, e.g. 150000000 -> "1.50000000".
func FormatAmount(a uint64) string {
	return fmt.Sprintf("%d.%08d", a/Bluglets, a%Bluglets)
}

// ParseAmount parses a decimal BLG string into bluglets. Accepts at most 8 decimals.
func ParseAmount(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "-+eE") {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	wholePart, fracPart, hasFrac := strings.Cut(s, ".")
	if hasFrac && len(fracPart) > 8 {
		return 0, fmt.Errorf("too many decimals in %q (max 8)", s)
	}
	whole, err := strconv.ParseUint(wholePart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	var frac uint64
	if hasFrac {
		padded := fracPart + strings.Repeat("0", 8-len(fracPart))
		frac, err = strconv.ParseUint(padded, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid amount %q", s)
		}
	}
	if whole > (math.MaxUint64-frac)/Bluglets {
		return 0, fmt.Errorf("amount overflow in %q", s)
	}
	return whole*Bluglets + frac, nil
}
