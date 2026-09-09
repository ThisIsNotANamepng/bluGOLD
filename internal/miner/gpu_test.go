//go:build gpu

package miner

import (
	"context"
	"testing"
	"time"

	"blugold/internal/chain"
	"blugold/internal/crypto"
	"blugold/internal/node"
)

// These tests only build with -tags gpu and only run their real work when an
// OpenCL GPU is actually present — that flag promises "GPU support is
// compiled in", not "this machine has a GPU", so no GPU means skip, not fail.

func TestGPUBackendMinesBlocks(t *testing.T) {
	if _, err := openGPU(); err != nil {
		t.Skipf("no usable OpenCL GPU on this machine: %v", err)
	}

	w, err := crypto.GenerateWallet()
	if err != nil {
		t.Fatal(err)
	}
	n, err := node.New(node.Config{
		Params:  chain.TestParams(),
		DataDir: t.TempDir(),
		Wallet:  w,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Stop()

	m := New(n, w, 1, 10*time.Millisecond, BackendGPU, 1<<16)
	if got := m.EnsureBackend(); got != BackendGPU {
		t.Fatalf("a GPU was available a moment ago but resolveBackend picked %v (%s)", got, m.BackendName())
	}
	t.Logf("mining on %s", m.BackendName())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && n.TipHeight() < 3 {
		time.Sleep(50 * time.Millisecond)
	}
	if n.TipHeight() < 3 {
		t.Fatalf("GPU miner produced only %d blocks", n.TipHeight())
	}
	if bal := n.GetAccount(w.Address()).Balance; bal == 0 {
		t.Fatal("GPU miner got no rewards")
	}
}

func TestAutoBackendBenchmarksGPU(t *testing.T) {
	if _, err := openGPU(); err != nil {
		t.Skipf("no usable OpenCL GPU on this machine: %v", err)
	}

	w, err := crypto.GenerateWallet()
	if err != nil {
		t.Fatal(err)
	}
	n, err := node.New(node.Config{
		Params:  chain.TestParams(),
		DataDir: t.TempDir(),
		Wallet:  w,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Stop()

	// Which backend wins is hardware-dependent; just check auto-selection
	// runs to completion and reports something usable.
	m := New(n, w, 4, 0, BackendAuto, 0)
	got := m.EnsureBackend()
	if got != BackendCPU && got != BackendGPU {
		t.Fatalf("resolveBackend left an unresolved backend: %v", got)
	}
	t.Logf("auto picked: %s", m.BackendName())
}
