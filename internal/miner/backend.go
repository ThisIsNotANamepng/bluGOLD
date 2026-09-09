package miner

import (
	"fmt"
	"strings"
)

// Backend selects which hardware a Miner searches nonces with.
type Backend int

const (
	// BackendAuto benchmarks CPU and GPU for a moment and mines with
	// whichever is faster, falling back to CPU when no GPU is usable.
	BackendAuto Backend = iota
	BackendCPU
	BackendGPU
)

func (b Backend) String() string {
	switch b {
	case BackendCPU:
		return "cpu"
	case BackendGPU:
		return "gpu"
	default:
		return "auto"
	}
}

// ParseBackend parses a --backend flag value.
func ParseBackend(s string) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return BackendAuto, nil
	case "cpu":
		return BackendCPU, nil
	case "gpu":
		return BackendGPU, nil
	default:
		return BackendAuto, fmt.Errorf("unknown mining backend %q (want auto, cpu, or gpu)", s)
	}
}

// gpuHandle is a running GPU mining backend bound to one device. The only
// implementation lives in gpu_opencl.go, built only with -tags gpu (it needs
// cgo and an OpenCL SDK); gpu_stub.go stands in otherwise so the default,
// dependency-free build always compiles.
type gpuHandle interface {
	// Name describes the bound device, for logging.
	Name() string
	// Search hashes `count` consecutive nonces starting at base against mid
	// and target (see targetWords), returning the first nonce satisfying
	// the target, if any.
	Search(mid gpuMidstate, target [8]uint32, base uint64, count uint32) (nonce uint64, found bool, err error)
	Close()
}
