// Package store persists blocks (append-only JSONL) and known peers.
package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"blugold/internal/chain"
	"blugold/internal/crypto"
)

type Store struct {
	dir string
	f   *os.File
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dataDir, "blocks.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Store{dir: dataDir, f: f}, nil
}

func (s *Store) Dir() string        { return s.dir }
func (s *Store) WalletPath() string { return crypto.WalletPath(s.dir) }

func (s *Store) AppendBlock(b *chain.Block) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = s.f.Write(data)
	return err
}

func (s *Store) LoadBlocks() ([]*chain.Block, error) {
	path := filepath.Join(s.dir, "blocks.jsonl")
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var blocks []*chain.Block
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var b chain.Block
		if err := json.Unmarshal(line, &b); err != nil {
			return nil, fmt.Errorf("corrupt block line: %w", err)
		}
		blocks = append(blocks, &b)
	}
	return blocks, sc.Err()
}

func (s *Store) peersPath() string { return filepath.Join(s.dir, "peers.json") }

func (s *Store) SavePeers(addrs []string) error {
	data, err := json.MarshalIndent(addrs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.peersPath() + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.peersPath())
}

func (s *Store) LoadPeers() ([]string, error) {
	data, err := os.ReadFile(s.peersPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var addrs []string
	return addrs, json.Unmarshal(data, &addrs)
}

func (s *Store) Close() error { return s.f.Close() }
