// Package miner runs a multi-threaded PoW mining loop against a node.
package miner

import (
	"context"
	"log"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"blugold/internal/chain"
	"blugold/internal/crypto"
	"blugold/internal/node"
)

type Miner struct {
	n        *node.Node
	addr     chain.Address
	threads  int
	minPause time.Duration
	nonce    atomic.Uint64
	total    atomic.Uint64
	rate     atomic.Uint64
}

func New(n *node.Node, w *crypto.Wallet, threads int, minPause time.Duration) *Miner {
	if threads < 1 {
		threads = 1
	}
	if minPause < 0 {
		minPause = 0
	}
	return &Miner{n: n, addr: w.Address(), threads: threads, minPause: minPause}
}

// Hashrate returns hashes per second over the last second.
func (m *Miner) Hashrate() uint64 { return m.rate.Load() }

func (m *Miner) trackLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var prev uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := m.total.Load()
			m.rate.Store(now - prev)
			prev = now
		}
	}
}

func (m *Miner) Run(ctx context.Context) {
	go m.trackLoop(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		m.mineOnce(ctx)
		if m.minPause > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(m.minPause):
			}
		}
	}
}

func (m *Miner) mineOnce(ctx context.Context) {
	n := m.n
	_, tips, cancel := n.SubscribeTips()
	defer cancel()

	cand := n.BuildCandidate(m.addr)
	if cand == nil {
		sleepCh(ctx, 500*time.Millisecond)
		return
	}
	target := n.Params().Target(cand.Difficulty)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	found := make(chan *chain.Block, 1)

	for i := 0; i < m.threads; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			work := *cand
			work.ExtraNonce = uint64(id)
			hv := new(big.Int)
			for {
				select {
				case <-stop:
					return
				default:
				}
				work.Nonce = m.nonce.Add(1)
				h := work.Hash()
				hv.SetBytes(h[:])
				m.total.Add(1)
				if hv.Cmp(target) < 0 {
					solution := work
					select {
					case found <- &solution:
					default:
					}
					return
				}
			}
		}(i)
	}

	select {
	case blk := <-found:
		close(stop)
		wg.Wait()
		if err := n.SubmitSolution(blk); err != nil {
			log.Printf("miner: mined block rejected: %v", err)
		}
	case newTip := <-tips:
		close(stop)
		wg.Wait()
		_ = newTip
	case <-ctx.Done():
		close(stop)
		wg.Wait()
	}
}

func sleepCh(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
