// Package state tracks account balances and nonces, and applies blocks.
package state

import (
	"errors"
	"fmt"

	"blugold/internal/chain"
)

type Account struct {
	Balance uint64 `json:"balance"`
	Nonce   uint64 `json:"nonce"`
}

// State is a mutable account map. Callers must Clone before mutating a shared snapshot.
type State struct {
	accounts map[chain.Address]*Account
}

func New() *State {
	return &State{accounts: make(map[chain.Address]*Account)}
}

func (s *State) Clone() *State {
	c := New()
	for a, acct := range s.accounts {
		c.accounts[a] = &Account{Balance: acct.Balance, Nonce: acct.Nonce}
	}
	return c
}

func (s *State) get(a chain.Address) *Account {
	acct, ok := s.accounts[a]
	if !ok {
		acct = &Account{}
		s.accounts[a] = acct
	}
	return acct
}

func (s *State) Account(a chain.Address) Account {
	if acct, ok := s.accounts[a]; ok {
		return *acct
	}
	return Account{}
}

// Accounts returns a copy of all non-empty accounts.
func (s *State) Accounts() map[chain.Address]Account {
	out := make(map[chain.Address]Account, len(s.accounts))
	for a, acct := range s.accounts {
		if acct.Balance != 0 || acct.Nonce != 0 {
			out[a] = *acct
		}
	}
	return out
}

func (s *State) Credit(a chain.Address, amt uint64) {
	if amt == 0 {
		return
	}
	acct := s.get(a)
	sum := acct.Balance + amt
	if sum < acct.Balance {
		panic("balance overflow")
	}
	acct.Balance = sum
}

// ApplyTx applies a non-coinbase tx: checks nonce and funds, then mutates state.
func (s *State) ApplyTx(t *chain.Tx) error {
	if t.IsCoinbase() {
		return errors.New("apply coinbase via block application")
	}
	acct := s.get(t.From)
	if t.Nonce != acct.Nonce {
		return fmt.Errorf("bad nonce: got %d, want %d", t.Nonce, acct.Nonce)
	}
	spend := t.Amount + t.Fee
	if spend < t.Amount {
		return errors.New("amount+fee overflow")
	}
	if acct.Balance < spend {
		return fmt.Errorf("insufficient funds: have %d, need %d", acct.Balance, spend)
	}
	acct.Balance -= spend
	acct.Nonce++
	s.Credit(t.To, t.Amount)
	return nil
}

// ValidateTx checks intrinsic validity plus nonce/balance against this state without mutating it.
func (s *State) ValidateTx(t *chain.Tx) error {
	if err := t.Verify(); err != nil {
		return err
	}
	acct := s.get(t.From)
	if t.Nonce != acct.Nonce {
		return fmt.Errorf("bad nonce: got %d, want %d", t.Nonce, acct.Nonce)
	}
	if acct.Balance < t.Amount+t.Fee {
		return errors.New("insufficient funds")
	}
	return nil
}

// ApplyBlock applies all txs in a validated block. Txs are applied first, coinbase last,
// so a miner cannot spend its own same-block reward.
func (s *State) ApplyBlock(b *chain.Block, p chain.Params) error {
	if len(b.Txs) == 0 || !b.Txs[0].IsCoinbase() {
		return errors.New("block has no coinbase")
	}
	var feeSum uint64
	for _, t := range b.Txs[1:] {
		if err := t.Verify(); err != nil {
			return fmt.Errorf("tx %s: %w", t.Hash().Short(), err)
		}
		if err := s.ApplyTx(t); err != nil {
			return fmt.Errorf("tx %s: %w", t.Hash().Short(), err)
		}
		feeSum += t.Fee
		if feeSum < t.Fee {
			return errors.New("fee sum overflow")
		}
	}
	cb := b.Txs[0]
	if err := cb.Verify(); err != nil {
		return fmt.Errorf("coinbase: %w", err)
	}
	want := p.RewardAt(b.Height) + feeSum
	if cb.Amount != want {
		return fmt.Errorf("coinbase pays %d, want %d (reward+fees)", cb.Amount, want)
	}
	s.Credit(cb.To, cb.Amount)
	return nil
}
