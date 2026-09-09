//go:build gpu

// Package miner's GPU backend, built only with `go build -tags gpu`. It is
// the one place in bluGOLD that isn't pure Go stdlib: it needs cgo and an
// OpenCL ICD (headers + libOpenCL.so/.dylib/.dll, and a driver exposing at
// least one GPU device) present on the build machine. Left out of the
// default build so `go build ./cmd/blugold` keeps working with nothing but
// a Go toolchain, per CONTEXT.md's zero-dependency rule; opting in is a
// deliberate, documented exception for people who actually have a GPU.
package miner

/*
#cgo LDFLAGS: -lOpenCL
#define CL_TARGET_OPENCL_VERSION 120
#include <CL/cl.h>
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

const gpuBuildTag = true

// kernelSource implements the exact same hash bluGOLD's consensus uses
// (single SHA-256 over Block.HeaderBytes()), but resumes from a
// precomputed midstate and only builds the final 64-byte block per trial —
// see sha256mid.go for why that's sufficient and correct.
const kernelSource = `
__constant uint K[64] = {
    0x428a2f98u,0x71374491u,0xb5c0fbcfu,0xe9b5dba5u,0x3956c25bu,0x59f111f1u,0x923f82a4u,0xab1c5ed5u,
    0xd807aa98u,0x12835b01u,0x243185beu,0x550c7dc3u,0x72be5d74u,0x80deb1feu,0x9bdc06a7u,0xc19bf174u,
    0xe49b69c1u,0xefbe4786u,0x0fc19dc6u,0x240ca1ccu,0x2de92c6fu,0x4a7484aau,0x5cb0a9dcu,0x76f988dau,
    0x983e5152u,0xa831c66du,0xb00327c8u,0xbf597fc7u,0xc6e00bf3u,0xd5a79147u,0x06ca6351u,0x14292967u,
    0x27b70a85u,0x2e1b2138u,0x4d2c6dfcu,0x53380d13u,0x650a7354u,0x766a0abbu,0x81c2c92eu,0x92722c85u,
    0xa2bfe8a1u,0xa81a664bu,0xc24b8b70u,0xc76c51a3u,0xd192e819u,0xd6990624u,0xf40e3585u,0x106aa070u,
    0x19a4c116u,0x1e376c08u,0x2748774cu,0x34b0bcb5u,0x391c0cb3u,0x4ed8aa4au,0x5b9cca4fu,0x682e6ff3u,
    0x748f82eeu,0x78a5636fu,0x84c87814u,0x8cc70208u,0x90befffau,0xa4506cebu,0xbef9a3f7u,0xc67178f2u
};

static inline uint rotr(uint x, uint n) { return (x >> n) | (x << (32u - n)); }

static inline uint bswap32(uint x) {
    return ((x & 0x000000FFu) << 24) | ((x & 0x0000FF00u) << 8) |
           ((x & 0x00FF0000u) >> 8)  | ((x & 0xFF000000u) >> 24);
}

__kernel void blugold_mine(
    __global const uint* midstate,      // 8 words
    __global const uint* blockTpl,      // 16 words, nonceWord/nonceWord+1 are placeholders
    const uint nonceWord,
    const ulong base,
    __global const uint* target,        // 8 words, most-significant first
    __global ulong* outNonce,
    volatile __global int* outFound)
{
    if (*outFound) return;

    ulong nonce = base + (ulong)get_global_id(0);
    uint w[64];
    for (int i = 0; i < 16; i++) w[i] = blockTpl[i];
    w[nonceWord]     = bswap32((uint)(nonce & 0xFFFFFFFFUL));
    w[nonceWord + 1] = bswap32((uint)(nonce >> 32));

    for (int i = 16; i < 64; i++) {
        uint s0 = rotr(w[i-15], 7) ^ rotr(w[i-15], 18) ^ (w[i-15] >> 3);
        uint s1 = rotr(w[i-2], 17) ^ rotr(w[i-2], 19) ^ (w[i-2] >> 10);
        w[i] = w[i-16] + s0 + w[i-7] + s1;
    }

    uint a = midstate[0], b = midstate[1], c = midstate[2], d = midstate[3];
    uint e = midstate[4], f = midstate[5], g = midstate[6], h = midstate[7];
    for (int i = 0; i < 64; i++) {
        uint S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
        uint ch = (e & f) ^ (~e & g);
        uint t1 = h + S1 + ch + K[i] + w[i];
        uint S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
        uint maj = (a & b) ^ (a & c) ^ (b & c);
        uint t2 = S0 + maj;
        h = g; g = f; f = e; e = d + t1; d = c; c = b; b = a; a = t1 + t2;
    }

    uint H[8];
    H[0]=midstate[0]+a; H[1]=midstate[1]+b; H[2]=midstate[2]+c; H[3]=midstate[3]+d;
    H[4]=midstate[4]+e; H[5]=midstate[5]+f; H[6]=midstate[6]+g; H[7]=midstate[7]+h;

    bool less = false;
    for (int i = 0; i < 8; i++) {
        if (H[i] < target[i]) { less = true; break; }
        if (H[i] > target[i]) { less = false; break; }
    }
    if (less) {
        if (atomic_cmpxchg(outFound, 0, 1) == 0) {
            *outNonce = nonce;
        }
    }
}
`

type openclGPU struct {
	ctx     C.cl_context
	queue   C.cl_command_queue
	program C.cl_program
	kernel  C.cl_kernel
	name    string

	midBuf, blockBuf, targetBuf, nonceOutBuf, foundOutBuf C.cl_mem
}

// openGPU picks the first GPU device on any OpenCL platform and compiles
// the mining kernel against it.
func openGPU() (gpuHandle, error) {
	var platforms [8]C.cl_platform_id
	var numPlatforms C.cl_uint
	if C.clGetPlatformIDs(8, &platforms[0], &numPlatforms) != C.CL_SUCCESS || numPlatforms == 0 {
		return nil, errors.New("opencl: no platforms found")
	}

	var device C.cl_device_id
	found := false
	for i := 0; i < int(numPlatforms); i++ {
		var devices [4]C.cl_device_id
		var numDevices C.cl_uint
		if C.clGetDeviceIDs(platforms[i], C.CL_DEVICE_TYPE_GPU, 4, &devices[0], &numDevices) == C.CL_SUCCESS && numDevices > 0 {
			device = devices[0]
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("opencl: no GPU device found")
	}

	var nameBuf [256]C.char
	C.clGetDeviceInfo(device, C.CL_DEVICE_NAME, 256, unsafe.Pointer(&nameBuf[0]), nil)
	name := C.GoString(&nameBuf[0])

	var status C.cl_int
	ctx := C.clCreateContext(nil, 1, &device, nil, nil, &status)
	if status != C.CL_SUCCESS {
		return nil, fmt.Errorf("opencl: clCreateContext: %d", status)
	}
	queue := C.clCreateCommandQueue(ctx, device, 0, &status)
	if status != C.CL_SUCCESS {
		C.clReleaseContext(ctx)
		return nil, fmt.Errorf("opencl: clCreateCommandQueue: %d", status)
	}

	csrc := C.CString(kernelSource)
	defer C.free(unsafe.Pointer(csrc))
	srcLen := C.size_t(len(kernelSource))
	program := C.clCreateProgramWithSource(ctx, 1, &csrc, &srcLen, &status)
	if status != C.CL_SUCCESS {
		C.clReleaseCommandQueue(queue)
		C.clReleaseContext(ctx)
		return nil, fmt.Errorf("opencl: clCreateProgramWithSource: %d", status)
	}
	if C.clBuildProgram(program, 1, &device, nil, nil, nil) != C.CL_SUCCESS {
		var logBuf [4096]C.char
		var logLen C.size_t
		C.clGetProgramBuildInfo(program, device, C.CL_PROGRAM_BUILD_LOG, 4096, unsafe.Pointer(&logBuf[0]), &logLen)
		C.clReleaseProgram(program)
		C.clReleaseCommandQueue(queue)
		C.clReleaseContext(ctx)
		return nil, fmt.Errorf("opencl: kernel build failed: %s", C.GoStringN(&logBuf[0], C.int(logLen)))
	}

	kname := C.CString("blugold_mine")
	kernel := C.clCreateKernel(program, kname, &status)
	C.free(unsafe.Pointer(kname))
	if status != C.CL_SUCCESS {
		C.clReleaseProgram(program)
		C.clReleaseCommandQueue(queue)
		C.clReleaseContext(ctx)
		return nil, fmt.Errorf("opencl: clCreateKernel: %d", status)
	}

	g := &openclGPU{ctx: ctx, queue: queue, program: program, kernel: kernel, name: name}
	mk := func(size int, flags C.cl_mem_flags) (C.cl_mem, error) {
		var st C.cl_int
		buf := C.clCreateBuffer(ctx, flags, C.size_t(size), nil, &st)
		if st != C.CL_SUCCESS {
			return nil, fmt.Errorf("opencl: clCreateBuffer: %d", st)
		}
		return buf, nil
	}
	var err error
	if g.midBuf, err = mk(8*4, C.CL_MEM_READ_ONLY); err != nil {
		g.Close()
		return nil, err
	}
	if g.blockBuf, err = mk(16*4, C.CL_MEM_READ_ONLY); err != nil {
		g.Close()
		return nil, err
	}
	if g.targetBuf, err = mk(8*4, C.CL_MEM_READ_ONLY); err != nil {
		g.Close()
		return nil, err
	}
	if g.nonceOutBuf, err = mk(8, C.CL_MEM_WRITE_ONLY); err != nil {
		g.Close()
		return nil, err
	}
	if g.foundOutBuf, err = mk(4, C.CL_MEM_READ_WRITE); err != nil {
		g.Close()
		return nil, err
	}
	return g, nil
}

func (g *openclGPU) Name() string { return g.name }

func (g *openclGPU) Search(mid gpuMidstate, target [8]uint32, base uint64, count uint32) (uint64, bool, error) {
	write := func(buf C.cl_mem, ptr unsafe.Pointer, size int) error {
		if C.clEnqueueWriteBuffer(g.queue, buf, C.CL_TRUE, 0, C.size_t(size), ptr, 0, nil, nil) != C.CL_SUCCESS {
			return errors.New("opencl: clEnqueueWriteBuffer failed")
		}
		return nil
	}
	if err := write(g.midBuf, unsafe.Pointer(&mid.H[0]), 8*4); err != nil {
		return 0, false, err
	}
	if err := write(g.blockBuf, unsafe.Pointer(&mid.Block[0]), 16*4); err != nil {
		return 0, false, err
	}
	if err := write(g.targetBuf, unsafe.Pointer(&target[0]), 8*4); err != nil {
		return 0, false, err
	}
	zero := C.cl_int(0)
	if err := write(g.foundOutBuf, unsafe.Pointer(&zero), 4); err != nil {
		return 0, false, err
	}

	// clSetKernelArg's cl_mem arguments are handles, not Go-managed memory,
	// but their Go type (C.cl_mem) is itself a pointer — taking &g.midBuf
	// would hand cgo's pointer checker "a Go pointer to a Go pointer" and
	// panic at runtime. Copying the handle's bits into a uintptr first (an
	// integer type cgo never treats as a pointer) sidesteps that safely.
	memArg := func(idx int, m C.cl_mem) error {
		raw := uintptr(unsafe.Pointer(m))
		if C.clSetKernelArg(g.kernel, C.cl_uint(idx), C.size_t(unsafe.Sizeof(m)), unsafe.Pointer(&raw)) != C.CL_SUCCESS {
			return fmt.Errorf("opencl: clSetKernelArg %d failed", idx)
		}
		return nil
	}
	scalarArg := func(idx int, size C.size_t, ptr unsafe.Pointer) error {
		if C.clSetKernelArg(g.kernel, C.cl_uint(idx), size, ptr) != C.CL_SUCCESS {
			return fmt.Errorf("opencl: clSetKernelArg %d failed", idx)
		}
		return nil
	}
	nonceWord := C.cl_uint(mid.NonceWord)
	baseArg := C.cl_ulong(base)
	if err := memArg(0, g.midBuf); err != nil {
		return 0, false, err
	}
	if err := memArg(1, g.blockBuf); err != nil {
		return 0, false, err
	}
	if err := scalarArg(2, C.size_t(unsafe.Sizeof(nonceWord)), unsafe.Pointer(&nonceWord)); err != nil {
		return 0, false, err
	}
	if err := scalarArg(3, C.size_t(unsafe.Sizeof(baseArg)), unsafe.Pointer(&baseArg)); err != nil {
		return 0, false, err
	}
	if err := memArg(4, g.targetBuf); err != nil {
		return 0, false, err
	}
	if err := memArg(5, g.nonceOutBuf); err != nil {
		return 0, false, err
	}
	if err := memArg(6, g.foundOutBuf); err != nil {
		return 0, false, err
	}

	global := C.size_t(count)
	if C.clEnqueueNDRangeKernel(g.queue, g.kernel, 1, nil, &global, nil, 0, nil, nil) != C.CL_SUCCESS {
		return 0, false, errors.New("opencl: clEnqueueNDRangeKernel failed")
	}
	var found C.cl_int
	if C.clEnqueueReadBuffer(g.queue, g.foundOutBuf, C.CL_TRUE, 0, 4, unsafe.Pointer(&found), 0, nil, nil) != C.CL_SUCCESS {
		return 0, false, errors.New("opencl: clEnqueueReadBuffer(found) failed")
	}
	if found == 0 {
		return 0, false, nil
	}
	var nonce C.cl_ulong
	if C.clEnqueueReadBuffer(g.queue, g.nonceOutBuf, C.CL_TRUE, 0, 8, unsafe.Pointer(&nonce), 0, nil, nil) != C.CL_SUCCESS {
		return 0, false, errors.New("opencl: clEnqueueReadBuffer(nonce) failed")
	}
	return uint64(nonce), true, nil
}

func (g *openclGPU) Close() {
	if g.kernel != nil {
		C.clReleaseKernel(g.kernel)
	}
	if g.program != nil {
		C.clReleaseProgram(g.program)
	}
	for _, buf := range []C.cl_mem{g.midBuf, g.blockBuf, g.targetBuf, g.nonceOutBuf, g.foundOutBuf} {
		if buf != nil {
			C.clReleaseMemObject(buf)
		}
	}
	if g.queue != nil {
		C.clReleaseCommandQueue(g.queue)
	}
	if g.ctx != nil {
		C.clReleaseContext(g.ctx)
	}
}
