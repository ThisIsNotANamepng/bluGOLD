// Package miner runs a PoW mining loop against a node, on the CPU or (with
// -tags gpu and an OpenCL device) the GPU.
package miner

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"blugold/internal/chain"
	"blugold/internal/crypto"
	"blugold/internal/node"
)

var errNotUsable = errors.New("miner: header shape not supported by the fast-path backend")

// defaultGPUBatch is how many nonces one GPU kernel dispatch searches. Small
// enough that a dispatch finishes in well under a second on modest hardware,
// so the miner still notices tip changes and shutdown promptly.
const defaultGPUBatch = 1 << 20

// benchmarkDuration is how long auto-backend selection spends timing each
// candidate backend.
const benchmarkDuration = 200 * time.Millisecond

type Miner struct {
	n        *node.Node
	addr     chain.Address
	threads  int
	minPause time.Duration
	backend  Backend // requested backend (possibly BackendAuto)
	gpuBatch uint32

	once   sync.Once
	active Backend // resolved backend (BackendCPU or BackendGPU), set once by ensureBackend
	gpu    gpuHandle

	nonce atomic.Uint64
	total atomic.Uint64
	rate  atomic.Uint64
}

// New creates a Miner. backend selects CPU, GPU, or (BackendAuto) whichever
// benchmarks faster on this machine; gpuBatch overrides the number of
// nonces per GPU dispatch (0 = defaultGPUBatch).
func New(n *node.Node, w *crypto.Wallet, threads int, minPause time.Duration, backend Backend, gpuBatch int) *Miner {
	if threads < 1 {
		threads = 1
	}
	if minPause < 0 {
		minPause = 0
	}
	if gpuBatch <= 0 {
		gpuBatch = defaultGPUBatch
	}
	return &Miner{n: n, addr: w.Address(), threads: threads, minPause: minPause, backend: backend, gpuBatch: uint32(gpuBatch)}
}

// Hashrate returns hashes per second over the last second.
func (m *Miner) Hashrate() uint64 { return m.rate.Load() }

// EnsureBackend resolves the requested backend (benchmarking CPU vs GPU for
// BackendAuto) if that hasn't happened yet, then returns the resolved
// backend. Safe to call before Run to learn what will actually be used
// (e.g. to print it), and safe to call more than once.
func (m *Miner) EnsureBackend() Backend {
	m.once.Do(m.resolveBackend)
	return m.active
}

// BackendName describes the resolved backend, including the GPU device name
// when applicable. It triggers backend resolution if that hasn't run yet.
func (m *Miner) BackendName() string {
	if m.EnsureBackend() == BackendGPU && m.gpu != nil {
		return "gpu (" + m.gpu.Name() + ")"
	}
	return m.active.String()
}

func (m *Miner) resolveBackend() {
	switch m.backend {
	case BackendCPU:
		m.active = BackendCPU
		log.Printf("miner: using CPU backend (%d threads)", m.threads)
	case BackendGPU:
		h, err := openGPU()
		if err != nil {
			log.Printf("miner: GPU backend requested but unavailable (%v); falling back to CPU", err)
			m.active = BackendCPU
			return
		}
		m.gpu = h
		m.active = BackendGPU
		log.Printf("miner: using GPU backend (%s)", h.Name())
	default: // BackendAuto
		h, err := openGPU()
		if err != nil {
			m.active = BackendCPU
			log.Printf("miner: no usable GPU (%v); using CPU backend (%d threads)", err, m.threads)
			return
		}
		cpuRate := m.benchmarkCPU(benchmarkDuration)
		gpuRate, gerr := m.benchmarkGPU(h)
		if gerr != nil || gpuRate <= cpuRate {
			h.Close()
			m.active = BackendCPU
			if gerr != nil {
				log.Printf("miner: GPU benchmark failed (%v); using CPU backend (%d threads, %s H/s)", gerr, m.threads, fmtRate(cpuRate))
			} else {
				log.Printf("miner: CPU faster (cpu %s H/s vs gpu %s H/s); using CPU backend (%d threads)", fmtRate(cpuRate), fmtRate(gpuRate), m.threads)
			}
			return
		}
		m.gpu = h
		m.active = BackendGPU
		log.Printf("miner: GPU faster (gpu %s H/s vs cpu %s H/s); using GPU backend (%s)", fmtRate(gpuRate), fmtRate(cpuRate), h.Name())
	}
}

// fmtRate renders a hashrate for log messages, same scaling as the CLI's info command.
func fmtRate(h uint64) string {
	switch {
	case h >= 1_000_000_000:
		return fmt.Sprintf("%.2fG", float64(h)/1e9)
	case h >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(h)/1e6)
	case h >= 1_000:
		return fmt.Sprintf("%.2fk", float64(h)/1e3)
	default:
		return fmt.Sprintf("%d", h)
	}
}

// benchmarkHeader synthesizes a header-shaped block for backend
// benchmarking, independent of any real chain state.
func benchmarkHeader(addr chain.Address) []byte {
	b := &chain.Block{Time: 1, Difficulty: 1, Miner: addr}
	return b.HeaderBytes()
}

// benchmarkCPU measures raw CPU hash throughput against a target no hash can
// satisfy, using m.threads goroutines.
func (m *Miner) benchmarkCPU(d time.Duration) uint64 {
	hdr := benchmarkHeader(m.addr)
	var total atomic.Uint64
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < m.threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			work := append([]byte(nil), hdr...)
			var n uint64
			for {
				select {
				case <-stop:
					return
				default:
				}
				binary.LittleEndian.PutUint64(work[len(work)-8:], n)
				chain.HashBytes(work)
				n++
				total.Add(1)
			}
		}()
	}
	time.Sleep(d)
	close(stop)
	wg.Wait()
	return uint64(float64(total.Load()) / d.Seconds())
}

// benchmarkGPU measures raw GPU hash throughput against a target no hash can
// satisfy (the all-zero target: no SHA-256 output is negative).
func (m *Miner) benchmarkGPU(h gpuHandle) (uint64, error) {
	hdr := benchmarkHeader(m.addr)
	mid, ok := buildMidstate(hdr)
	if !ok {
		return 0, errNotUsable
	}
	var target [8]uint32
	deadline := time.Now().Add(benchmarkDuration)
	var total uint64
	var base uint64
	for time.Now().Before(deadline) {
		_, found, err := h.Search(mid, target, base, m.gpuBatch)
		if err != nil {
			return 0, err
		}
		if found {
			break
		}
		base += uint64(m.gpuBatch)
		total += uint64(m.gpuBatch)
	}
	return uint64(float64(total) / benchmarkDuration.Seconds()), nil
}

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
	m.EnsureBackend()
	if m.gpu != nil {
		defer func() {
			m.gpu.Close()
			m.gpu = nil
		}()
	}
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

	if m.active == BackendGPU {
		m.mineOnceGPU(ctx, cand, target, tips)
		return
	}
	m.mineOnceCPU(ctx, cand, target, tips)
}

func (m *Miner) mineOnceCPU(ctx context.Context, cand *chain.Block, target *big.Int, tips <-chan *chain.Block) {
	n := m.n
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

// mineOnceGPU searches nonces for cand on the GPU, one dispatch of
// m.gpuBatch nonces at a time, until a solution is found, the tip changes
// underneath the candidate, or ctx is cancelled. If the GPU can't handle
// this header's shape (never true for bluGOLD's current wire format) or hits
// a runtime error, it falls back to the CPU path for this round and every
// round after (the GPU handle is retired).
func (m *Miner) mineOnceGPU(ctx context.Context, cand *chain.Block, target *big.Int, tips <-chan *chain.Block) {
	work := *cand
	work.ExtraNonce = 0
	mid, ok := buildMidstate(work.HeaderBytes())
	if !ok {
		log.Printf("miner: GPU backend can't handle this header shape; using CPU for this round")
		m.mineOnceCPU(ctx, cand, target, tips)
		return
	}
	tw := targetWords(target)

	var base uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-tips:
			return
		default:
		}
		nonce, found, err := m.gpu.Search(mid, tw, base, m.gpuBatch)
		m.total.Add(uint64(m.gpuBatch))
		if err != nil {
			log.Printf("miner: GPU search failed (%v); falling back to CPU", err)
			m.gpu.Close()
			m.gpu = nil
			m.active = BackendCPU
			m.mineOnceCPU(ctx, cand, target, tips)
			return
		}
		if found {
			work.Nonce = nonce
			solution := work
			if err := m.n.SubmitSolution(&solution); err != nil {
				log.Printf("miner: mined block rejected: %v", err)
			}
			return
		}
		base += uint64(m.gpuBatch)
	}
}

func sleepCh(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
