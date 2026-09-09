//go:build !gpu

package miner

import "errors"

// gpuBuildTag reports whether this binary was built with GPU support
// (-tags gpu). It is false here; see gpu_opencl.go for the real backend.
const gpuBuildTag = false

func openGPU() (gpuHandle, error) {
	return nil, errors.New("GPU mining not compiled into this binary: rebuild with \"go build -tags gpu\" (requires an OpenCL SDK)")
}
