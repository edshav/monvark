// Copyright (c) 2026 The Decred developers.

// Package cl binds the nineteen OpenCL entry points this miner calls, resolving
// them from the vendor's shared library at run time rather than linking against
// it.  Nothing here is a general purpose OpenCL binding: it is an internal
// detail of one binary, and an entry point belongs in it only when a call site
// exists.
//
// Loading at run time is what lets the miner be built without a vendor SDK, be
// cross compiled with CGO_ENABLED=0, and — most importantly — start on a machine
// with no OpenCL driver at all, so it can report that as an error instead of
// failing to launch.
package cl

import (
	"bytes"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Opaque handles.  They are distinct types so that, say, a context cannot be
// passed where a queue is wanted.
type (
	// PlatformID identifies an OpenCL platform.
	PlatformID uintptr
	// DeviceID identifies an OpenCL device.
	DeviceID uintptr
	// Context identifies an OpenCL context.
	Context uintptr
	// Queue identifies an OpenCL command queue.
	Queue uintptr
	// Mem identifies an OpenCL memory object.
	Mem uintptr
	// Program identifies an OpenCL program.
	Program uintptr
	// Kernel identifies an OpenCL kernel.
	Kernel uintptr
)

// Parameter types, sized to match the C types they stand for: cl_bitfield is
// 64 bits and cl_uint is 32.
type (
	// DeviceType is a bitfield of OpenCL device kinds (cl_device_type).
	DeviceType uint64
	// DeviceInfo names a device query parameter (cl_device_info).
	DeviceInfo uint32
	// MemFlags is a bitfield of OpenCL memory allocation flags (cl_mem_flags).
	MemFlags uint64
)

// Statuses returned by the entry points below.  Only the ones with call sites
// are named; ErrorName renders the rest.
const (
	Success        int32 = 0
	DeviceNotFound int32 = -1
	InvalidValue   int32 = -30
	// PlatformNotFound is CL_PLATFORM_NOT_FOUND_KHR, which the ocl-icd loader
	// returns when it is installed but no vendor ICD is.
	PlatformNotFound int32 = -1001
)

// Device types, as returned by CL_DEVICE_TYPE and accepted by clGetDeviceIDs.
const (
	DeviceTypeCPU DeviceType = 1 << 1
	DeviceTypeGPU DeviceType = 1 << 2
	DeviceTypeAll DeviceType = 0xFFFFFFFF
)

// Device parameters readable via DeviceInfoString and DeviceInfoUint64.
const (
	DeviceName      DeviceInfo = 0x102B
	DeviceTypeParam DeviceInfo = 0x1000
)

// MemReadWrite is the only buffer access mode this miner creates.
const MemReadWrite MemFlags = 1 << 0

// programBuildLog is CL_PROGRAM_BUILD_LOG, the only program build parameter
// read here.
const programBuildLog uint32 = 0x1183

// The bound entry points.  Every signature uses explicitly sized types because
// purego maps Go types to C types by width, which makes int and uint ambiguous;
// the handle and parameter types above are all defined over sized types and so
// are safe to name directly.
//
// These are nil until Load succeeds.  Thirteen of them need no wrapper at all:
// purego passes a Go slice as its data pointer (a nil slice becomes NULL, which
// is OpenCL's own "no value" convention) and null terminates a Go string into
// char* itself, so the function variable is the API.
var (
	GetPlatformIDs func(numEntries uint32, platforms []PlatformID, numPlatforms *uint32) int32
	GetDeviceIDs   func(platform PlatformID, deviceType DeviceType, numEntries uint32,
		devices []DeviceID, numDevices *uint32) int32
	CreateContext func(properties *uintptr, numDevices uint32, devices []DeviceID,
		notify, userData uintptr, errcode *int32) Context
	CreateCommandQueue func(ctx Context, device DeviceID, properties uint64, errcode *int32) Queue
	CreateBuffer       func(ctx Context, flags MemFlags, size uint64, hostPtr unsafe.Pointer,
		errcode *int32) Mem
	BuildProgram func(program Program, numDevices uint32, devices []DeviceID,
		options string, notify, userData uintptr) int32
	CreateKernel        func(program Program, name string, errcode *int32) Kernel
	SetKernelArg        func(kernel Kernel, index uint32, size uint64, value unsafe.Pointer) int32
	ReleaseMemObject    func(mem Mem) int32
	ReleaseKernel       func(kernel Kernel) int32
	ReleaseProgram      func(program Program) int32
	ReleaseCommandQueue func(queue Queue) int32
	ReleaseContext      func(ctx Context) int32
)

// The six entry points that are wrapped rather than exposed directly: the two
// size probe reads, the char** program source, and the three enqueue calls
// whose event parameters are always nil here.
var (
	getDeviceInfo func(device DeviceID, name DeviceInfo, size uint64, value unsafe.Pointer,
		sizeRet *uint64) int32
	getProgramBuildInfo func(program Program, device DeviceID, name uint32, size uint64,
		value unsafe.Pointer, sizeRet *uint64) int32
	createProgramWithSource func(ctx Context, count uint32, sources **byte, lengths *uint64,
		errcode *int32) Program
	enqueueNDRangeKernel func(queue Queue, kernel Kernel, workDim uint32,
		globalOffset, globalSize, localSize *uint64,
		numEvents uint32, waitList, event uintptr) int32
	enqueueReadBuffer func(queue Queue, buffer Mem, blocking uint32, offset, size uint64,
		ptr unsafe.Pointer, numEvents uint32, waitList, event uintptr) int32
	enqueueWriteBuffer func(queue Queue, buffer Mem, blocking uint32, offset, size uint64,
		ptr unsafe.Pointer, numEvents uint32, waitList, event uintptr) int32
)

// errorNames maps an OpenCL status to its symbolic name.  Copied from the go-cl
// binding this package replaces; see LICENSE.
var errorNames = map[int32]string{
	0: "CL_SUCCESS", -1: "CL_DEVICE_NOT_FOUND", -2: "CL_DEVICE_NOT_AVAILABLE",
	-3: "CL_COMPILER_NOT_AVAILABLE", -4: "CL_MEM_OBJECT_ALLOCATION_FAILURE",
	-5: "CL_OUT_OF_RESOURCES", -6: "CL_OUT_OF_HOST_MEMORY",
	-11: "CL_BUILD_PROGRAM_FAILURE", -12: "CL_MAP_FAILURE",
	-30: "CL_INVALID_VALUE", -31: "CL_INVALID_DEVICE_TYPE",
	-32: "CL_INVALID_PLATFORM", -33: "CL_INVALID_DEVICE",
	-34: "CL_INVALID_CONTEXT", -35: "CL_INVALID_QUEUE_PROPERTIES",
	-36: "CL_INVALID_COMMAND_QUEUE", -38: "CL_INVALID_MEM_OBJECT",
	-42: "CL_INVALID_BINARY", -43: "CL_INVALID_BUILD_OPTIONS",
	-44: "CL_INVALID_PROGRAM", -45: "CL_INVALID_PROGRAM_EXECUTABLE",
	-46: "CL_INVALID_KERNEL_NAME", -47: "CL_INVALID_KERNEL_DEFINITION",
	-48: "CL_INVALID_KERNEL", -49: "CL_INVALID_ARG_INDEX",
	-50: "CL_INVALID_ARG_VALUE", -51: "CL_INVALID_ARG_SIZE",
	-52: "CL_INVALID_KERNEL_ARGS", -53: "CL_INVALID_WORK_DIMENSION",
	-54: "CL_INVALID_WORK_GROUP_SIZE", -55: "CL_INVALID_WORK_ITEM_SIZE",
	-56: "CL_INVALID_GLOBAL_OFFSET", -58: "CL_INVALID_EVENT",
	-59: "CL_INVALID_OPERATION", -61: "CL_INVALID_BUFFER_SIZE",
	-63:   "CL_INVALID_GLOBAL_WORK_SIZE",
	-1001: "CL_PLATFORM_NOT_FOUND_KHR",
}

// ErrorName returns the symbolic name of an OpenCL status, or the number when
// it is not one this package knows.
func ErrorName(status int32) string {
	if name, ok := errorNames[status]; ok {
		return name
	}
	return "unknown OpenCL error " + strconv.Itoa(int(status))
}

// DeviceInfoString returns a string-valued device parameter such as DeviceName.
func DeviceInfoString(device DeviceID, name DeviceInfo) (string, int32) {
	var size uint64
	if status := getDeviceInfo(device, name, 0, nil, &size); status != Success {
		return "", status
	}
	if size == 0 {
		return "", Success
	}

	buf := make([]byte, size)
	if status := getDeviceInfo(device, name, size, unsafe.Pointer(&buf[0]), nil); status != Success {
		return "", status
	}
	return string(bytes.TrimRight(buf, "\x00")), Success
}

// DeviceInfoUint64 returns a scalar device parameter.  Every one this miner
// reads is a cl_bitfield, which the specification fixes at 8 bytes.
func DeviceInfoUint64(device DeviceID, name DeviceInfo) (uint64, int32) {
	var value uint64
	status := getDeviceInfo(device, name, 8, unsafe.Pointer(&value), nil)
	return value, status
}

// CreateProgramWithSource creates a program from one source string, which is
// all this miner needs.
func CreateProgramWithSource(ctx Context, source string, errcode *int32) Program {
	src := []byte(source)
	if len(src) == 0 {
		*errcode = InvalidValue
		return 0
	}

	ptr := &src[0]
	length := uint64(len(src))
	program := createProgramWithSource(ctx, 1, &ptr, &length, errcode)
	runtime.KeepAlive(src)
	return program
}

// ProgramBuildLog returns the compiler output for a program on one device.
func ProgramBuildLog(program Program, device DeviceID) (string, int32) {
	var size uint64
	status := getProgramBuildInfo(program, device, programBuildLog, 0, nil, &size)
	if status != Success {
		return "", status
	}
	if size == 0 {
		return "", Success
	}

	buf := make([]byte, size)
	status = getProgramBuildInfo(program, device, programBuildLog, size,
		unsafe.Pointer(&buf[0]), nil)
	if status != Success {
		return "", status
	}
	return string(bytes.TrimRight(buf, "\x00")), Success
}

// EnqueueNDRangeKernel enqueues a one dimensional kernel launch with no global
// offset, which is the only shape either call site uses.
func EnqueueNDRangeKernel(queue Queue, kernel Kernel, globalWorkSize, localWorkSize uint64) int32 {
	return enqueueNDRangeKernel(queue, kernel, 1, nil, &globalWorkSize,
		&localWorkSize, 0, 0, 0)
}

// EnqueueReadBuffer reads from a device buffer into host memory.
func EnqueueReadBuffer(queue Queue, buffer Mem, blocking bool, offset, size uint64,
	ptr unsafe.Pointer) int32 {

	return enqueueReadBuffer(queue, buffer, clBool(blocking), offset, size, ptr, 0, 0, 0)
}

// EnqueueWriteBuffer writes host memory into a device buffer.
func EnqueueWriteBuffer(queue Queue, buffer Mem, blocking bool, offset, size uint64,
	ptr unsafe.Pointer) int32 {

	return enqueueWriteBuffer(queue, buffer, clBool(blocking), offset, size, ptr, 0, 0, 0)
}

// clBool converts to cl_bool, which is a cl_uint and therefore 4 bytes rather
// than the single byte a Go bool would be passed as.
func clBool(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// Load resolves the OpenCL library and binds the entry points this package
// calls.  When path is non-empty it is the only candidate tried, which is the
// escape hatch for installations the built-in list misses.
func Load(path string) error {
	candidates := libraryCandidates
	if path != "" {
		candidates = []string{path}
	}

	var handle uintptr
	attempts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		h, err := openLibrary(candidate)
		if err != nil {
			attempts = append(attempts, fmt.Sprintf("%s: %v", candidate, err))
			continue
		}
		handle = h
		break
	}
	if handle == 0 {
		return fmt.Errorf("unable to load the OpenCL library.  Tried:\n\t%s\n"+
			"Install your GPU vendor's OpenCL driver, or pass --opencl-lib with "+
			"the full path to it", strings.Join(attempts, "\n\t"))
	}

	return bind(handle)
}

// bind resolves every entry point.  purego panics when a symbol is missing,
// which for a library that loaded but is the wrong one would surface as a
// crash; convert it into an error naming the symbol.
func bind(handle uintptr) (err error) {
	var current string
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the OpenCL library does not provide %s: %v", current, r)
		}
	}()

	reg := func(name string, fptr any) {
		current = name
		purego.RegisterLibFunc(fptr, handle, name)
	}

	reg("clGetPlatformIDs", &GetPlatformIDs)
	reg("clGetDeviceIDs", &GetDeviceIDs)
	reg("clGetDeviceInfo", &getDeviceInfo)
	reg("clCreateContext", &CreateContext)
	reg("clCreateCommandQueue", &CreateCommandQueue)
	reg("clCreateBuffer", &CreateBuffer)
	reg("clCreateProgramWithSource", &createProgramWithSource)
	reg("clBuildProgram", &BuildProgram)
	reg("clGetProgramBuildInfo", &getProgramBuildInfo)
	reg("clCreateKernel", &CreateKernel)
	reg("clSetKernelArg", &SetKernelArg)
	reg("clEnqueueNDRangeKernel", &enqueueNDRangeKernel)
	reg("clEnqueueReadBuffer", &enqueueReadBuffer)
	reg("clEnqueueWriteBuffer", &enqueueWriteBuffer)
	reg("clReleaseMemObject", &ReleaseMemObject)
	reg("clReleaseKernel", &ReleaseKernel)
	reg("clReleaseProgram", &ReleaseProgram)
	reg("clReleaseCommandQueue", &ReleaseCommandQueue)
	reg("clReleaseContext", &ReleaseContext)

	return nil
}
