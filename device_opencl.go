// Copyright (c) 2016-2023 The Decred developers.

package main

import (
	"context"
	_ "embed"
	"fmt"
	"math"
	"os"
	"sync"
	"time"
	"unsafe"

	"github.com/decred/gominer/cl"
	"github.com/decred/gominer/util"
	"github.com/decred/gominer/work"
)

// kernelSource is the OpenCL kernel, compiled into the binary.  --kernel used
// to point at this file on disk and defaulted to a bare relative path, so the
// miner only ran from its own build directory.
//
//go:embed blake3.cl
var kernelSource string

// Return the GPU library in use.
func gpuLib() string {
	return "OpenCL"
}

const (
	outputBufferSize = cl.CL_size_t(64)
	localWorksize    = 64
	uint32Size       = cl.CL_size_t(unsafe.Sizeof(cl.CL_uint(0)))
)

var zeroSlice = []cl.CL_uint{cl.CL_uint(0)}

func appendBitfield(info, value cl.CL_bitfield, name string, str *string) {
	if (info & value) != 0 {
		*str += name
	}
}

func clError(status cl.CL_int, f string) error {
	if -status < 0 || int(-status) > len(cl.ERROR_CODES_STRINGS) {
		return fmt.Errorf("returned unknown error")
	}

	return fmt.Errorf("%s returned error %s (%d)", f,
		cl.ERROR_CODES_STRINGS[-status], status)
}

type Device struct {
	sync.Mutex
	index int

	// Items for OpenCL device
	platformID   cl.CL_platform_id
	deviceID     cl.CL_device_id
	deviceName   string
	deviceType   string
	context      cl.CL_context
	queue        cl.CL_command_queue
	outputBuffer cl.CL_mem
	program      cl.CL_program
	kernel       cl.CL_kernel

	workSize uint32

	// extraNonce is an additional nonce that is used to separate groups of
	// devices into exclusive ranges to ensure multiple groups do not duplicate
	// work.
	//
	// For solo mining, it is unique per device.
	//
	// For pool mining, it is assigned by the pool on a per-connection basis and
	// therefore is only unique per client.  Note that this means it will be the
	// same for all devices with pool mining.
	extraNonce uint32

	// extraNonce2 is a per device additional nonce where the first byte is the
	// device ID (offset by a per-process random value) and the last 3 bytes are
	// dedicated to the search space.  Note that this means up to 256 devices
	// are supported without the possibility of duplicate work.
	//
	// Since the first byte is unique per device, it does not change during
	// operation which implies this value will rollover to 0x??000000 from
	// 0x??ffffff.
	extraNonce2 uint32

	midstate  [8]uint32
	lastBlock [16]uint32

	work     work.Work
	newWork  chan *work.Work
	workDone chan []byte
	hasWork  bool

	started          uint32
	allDiffOneShares uint64
	validShares      uint64
	invalidShares    uint64
}

func getCLPlatforms() ([]cl.CL_platform_id, error) {
	var numPlatforms cl.CL_uint
	status := cl.CLGetPlatformIDs(0, nil, &numPlatforms)
	if status != cl.CL_SUCCESS {
		return nil, clError(status, "CLGetPlatformIDs")
	}
	platforms := make([]cl.CL_platform_id, numPlatforms)
	status = cl.CLGetPlatformIDs(numPlatforms, platforms, nil)
	if status != cl.CL_SUCCESS {
		return nil, clError(status, "CLGetPlatformIDs")
	}
	return platforms, nil
}

// getCLDevices returns the list of devices for the given platform.
func getCLDevices(platform cl.CL_platform_id) ([]cl.CL_device_id, error) {
	var numDevices cl.CL_uint
	status := cl.CLGetDeviceIDs(platform, cl.CL_DEVICE_TYPE_ALL, 0, nil,
		&numDevices)
	if status != cl.CL_SUCCESS && status != cl.CL_DEVICE_NOT_FOUND {
		return nil, clError(status, "CLGetDeviceIDs")
	}
	if numDevices == 0 {
		return nil, nil
	}
	devices := make([]cl.CL_device_id, numDevices)
	status = cl.CLGetDeviceIDs(platform, cl.CL_DEVICE_TYPE_ALL, numDevices,
		devices, nil)
	if status != cl.CL_SUCCESS {
		return nil, clError(status, "CLGetDeviceIDs")
	}
	return devices, nil
}

// ListDevices prints a list of devices present.
func ListDevices() {
	platformIDs, err := getCLPlatforms()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not get CL platforms: %v\n", err)
		os.Exit(1)
	}

	deviceListIndex := 0
	for i := range platformIDs {
		platformID := platformIDs[i]
		deviceIDs, err := getCLDevices(platformID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not get CL devices for platform: %v\n", err)
			os.Exit(1)
		}
		for _, deviceID := range deviceIDs {
			fmt.Printf("DEV #%d: %s\n", deviceListIndex, getDeviceInfo(deviceID, cl.CL_DEVICE_NAME, "CL_DEVICE_NAME"))
			deviceListIndex++
		}

	}
}

func NewDevice(index int, order int, platformID cl.CL_platform_id, deviceID cl.CL_device_id,
	workDone chan []byte) (*Device, error) {
	d := &Device{
		index:      index,
		platformID: platformID,
		deviceID:   deviceID,
		deviceName: getDeviceInfo(deviceID, cl.CL_DEVICE_NAME, "CL_DEVICE_NAME"),
		deviceType: getDeviceInfo(deviceID, cl.CL_DEVICE_TYPE, "CL_DEVICE_TYPE"),
		newWork:    make(chan *work.Work, 5),
		workDone:   workDone,
	}

	var status cl.CL_int

	// Create the CL context.
	d.context = cl.CLCreateContext(nil, 1, []cl.CL_device_id{deviceID},
		nil, nil, &status)
	if status != cl.CL_SUCCESS {
		return nil, clError(status, "CLCreateContext")
	}

	// Create the command queue.
	d.queue = cl.CLCreateCommandQueue(d.context, deviceID, 0, &status)
	if status != cl.CL_SUCCESS {
		return nil, clError(status, "CLCreateCommandQueue")
	}

	// Create the output buffer.
	d.outputBuffer = cl.CLCreateBuffer(d.context, cl.CL_MEM_READ_WRITE,
		uint32Size*outputBufferSize, nil, &status)
	if status != cl.CL_SUCCESS {
		return nil, clError(status, "CLCreateBuffer")
	}

	// Create the program from the embedded kernel source.
	progSrc := [][]byte{[]byte(kernelSource)}
	progSize := []cl.CL_size_t{cl.CL_size_t(len(kernelSource))}
	d.program = cl.CLCreateProgramWithSource(d.context, 1, progSrc, progSize, &status)
	if status != cl.CL_SUCCESS {
		return nil, clError(status, "CLCreateProgramWithSource")
	}

	// Build the program for the device.
	compilerOptions := ""
	compilerOptions += fmt.Sprintf(" -D WORKSIZE=%d", localWorksize)
	status = cl.CLBuildProgram(d.program, 1, []cl.CL_device_id{deviceID},
		[]byte(compilerOptions), nil, nil)
	if status != cl.CL_SUCCESS {
		err := clError(status, "CLBuildProgram")

		// Something went wrong! Print what it is.
		var logSize cl.CL_size_t
		status = cl.CLGetProgramBuildInfo(d.program, deviceID,
			cl.CL_PROGRAM_BUILD_LOG, 0, nil, &logSize)
		if status != cl.CL_SUCCESS {
			minrLog.Errorf("Could not obtain compilation error log: %v",
				clError(status, "CLGetProgramBuildInfo"))
		}
		var programLog interface{}
		status = cl.CLGetProgramBuildInfo(d.program, deviceID,
			cl.CL_PROGRAM_BUILD_LOG, logSize, &programLog, nil)
		if status != cl.CL_SUCCESS {
			minrLog.Errorf("Could not obtain compilation error log: %v",
				clError(status, "CLGetProgramBuildInfo"))
		}
		minrLog.Errorf("%s\n", programLog)

		return nil, err
	}

	// Create the kernel.
	d.kernel = cl.CLCreateKernel(d.program, []byte("search"), &status)
	if status != cl.CL_SUCCESS {
		return nil, clError(status, "CLCreateKernel")
	}

	d.started = uint32(time.Now().Unix())

	// Autocalibrate the desired work size for the kernel, or use one of the
	// values passed explicitly by the use.
	// The intensity or worksize must be set by the user.
	userSetWorkSize := false
	if len(cfg.IntensityInts) > 0 || len(cfg.WorkSizeInts) > 0 {
		userSetWorkSize = true
	}

	var globalWorkSize uint32
	if !userSetWorkSize {
		// Apply the first setting as a global setting
		calibrateTime := cfg.AutocalibrateInts[0]

		// Override with the per-device setting if it exists
		for i := range cfg.AutocalibrateInts {
			if i == order {
				calibrateTime = cfg.AutocalibrateInts[i]
			}
		}

		idealWorkSize, err := d.calcWorkSizeForMilliseconds(calibrateTime)
		if err != nil {
			return nil, err
		}

		minrLog.Debugf("Autocalibration successful, work size for %v"+
			"ms per kernel execution on device %v determined to be %v",
			calibrateTime, d.index, idealWorkSize)

		globalWorkSize = idealWorkSize
	} else {
		if len(cfg.IntensityInts) > 0 {
			// Apply the first setting as a global setting
			globalWorkSize = 1 << uint32(cfg.IntensityInts[0])

			// Override with the per-device setting if it exists
			for i := range cfg.IntensityInts {
				if i == order {
					globalWorkSize = 1 << uint32(cfg.IntensityInts[order])
				}
			}
		}
		if len(cfg.WorkSizeInts) > 0 {
			// Apply the first setting as a global setting
			globalWorkSize = cfg.WorkSizeInts[0]

			// Override with the per-device setting if it exists
			for i := range cfg.WorkSizeInts {
				if i == order {
					globalWorkSize = cfg.WorkSizeInts[order]
				}
			}

		}
	}
	intensity := math.Log2(float64(globalWorkSize))
	minrLog.Infof("DEV #%d: Work size set to %v ('intensity' %v)",
		d.index, globalWorkSize, intensity)
	d.workSize = globalWorkSize

	return d, nil
}

func (d *Device) runDevice(ctx context.Context) error {
	minrLog.Infof("Started DEV #%d: %s", d.index, d.deviceName)
	outputData := make([]uint32, outputBufferSize)

	// Initialize the nonces for the device such that each device in the same
	// system is doing different work while also helping prevent collisions
	// across multiple processes and systems working on the same template.
	if err := d.initNonces(); err != nil {
		return err
	}

	var status cl.CL_int
	ctxDoneCh := ctx.Done()
	for {
		d.updateCurrentWork(ctx)

		select {
		case <-ctxDoneCh:
			return nil
		default:
		}

		// Increment second extra nonce while respecting the device id.
		util.RolloverExtraNonce(&d.extraNonce2)
		d.lastBlock[work.Nonce2Word] = d.extraNonce2

		// Update the timestamp.
		diffSeconds := uint32(time.Now().Unix()) - d.work.TimeReceived
		ts := d.work.JobTime + diffSeconds
		d.lastBlock[work.TimestampWord] = ts

		// arg 0: pointer to the buffer
		obuf := d.outputBuffer
		status = cl.CLSetKernelArg(d.kernel, 0,
			cl.CL_size_t(unsafe.Sizeof(obuf)),
			unsafe.Pointer(&obuf))
		if status != cl.CL_SUCCESS {
			return clError(status, "CLSetKernelArg")
		}

		// args 1..8: midstate
		for i := 0; i < 8; i++ {
			ms := d.midstate[i]
			status = cl.CLSetKernelArg(d.kernel, cl.CL_uint(i+1),
				uint32Size, unsafe.Pointer(&ms))
			if status != cl.CL_SUCCESS {
				return clError(status, "CLSetKernelArg")
			}
		}

		// args 9..20: lastBlock except nonce
		i2 := 0
		for i := 0; i < 12; i++ {
			if i2 == work.Nonce0Word {
				i2++
			}
			lb := d.lastBlock[i2]
			status = cl.CLSetKernelArg(d.kernel, cl.CL_uint(i+9),
				uint32Size, unsafe.Pointer(&lb))
			if status != cl.CL_SUCCESS {
				return clError(status, "CLSetKernelArg")
			}
			i2++
		}

		// Clear the found count from the buffer
		status = cl.CLEnqueueWriteBuffer(d.queue, d.outputBuffer,
			cl.CL_FALSE, 0, uint32Size, unsafe.Pointer(&zeroSlice[0]),
			0, nil, nil)
		if status != cl.CL_SUCCESS {
			return clError(status, "CLEnqueueWriteBuffer")
		}

		// Execute the kernel and follow its execution time.
		currentTime := time.Now()
		var globalWorkSize [1]cl.CL_size_t
		globalWorkSize[0] = cl.CL_size_t(d.workSize)
		var localWorkSize [1]cl.CL_size_t
		localWorkSize[0] = localWorksize
		status = cl.CLEnqueueNDRangeKernel(d.queue, d.kernel, 1, nil,
			globalWorkSize[:], localWorkSize[:], 0, nil, nil)
		if status != cl.CL_SUCCESS {
			return clError(status, "CLEnqueueNDRangeKernel")
		}

		// Read the output buffer.
		cl.CLEnqueueReadBuffer(d.queue, d.outputBuffer, cl.CL_TRUE, 0,
			uint32Size*outputBufferSize, unsafe.Pointer(&outputData[0]), 0,
			nil, nil)
		if status != cl.CL_SUCCESS {
			return clError(status, "CLEnqueueReadBuffer")
		}

		for i := uint32(0); i < outputData[0]; i++ {
			minrLog.Debugf("DEV #%d: Found candidate %v nonce %08x, "+
				"extraNonce %08x, extraNonce2 %08x, timestamp %08x",
				d.index, i+1, outputData[i+1], d.lastBlock[work.Nonce1Word],
				d.lastBlock[work.Nonce2Word], d.lastBlock[work.TimestampWord])

			// Assess the work. If it's below target, it'll be rejected
			// here. The mining algorithm currently sends this function any
			// difficulty 1 shares.
			d.foundCandidate(d.lastBlock[work.TimestampWord], outputData[i+1],
				d.lastBlock[work.Nonce1Word], d.lastBlock[work.Nonce2Word])
		}

		elapsedTime := time.Since(currentTime)
		minrLog.Tracef("DEV #%d: Kernel execution to read time: %v", d.index,
			elapsedTime)
	}
}

func newMinerDevs(workDone chan []byte) ([]*Device, error) {
	deviceListIndex := 0
	deviceListEnabledCount := 0

	platformIDs, err := getCLPlatforms()
	if err != nil {
		return nil, fmt.Errorf("could not get CL platforms: %w", err)
	}

	var devices []*Device
	for p := range platformIDs {
		platformID := platformIDs[p]
		CLdeviceIDs, err := getCLDevices(platformID)
		if err != nil {
			return nil, fmt.Errorf("could not get CL devices for platform: %w", err)
		}

		for _, CLdeviceID := range CLdeviceIDs {
			miningAllowed := false

			// Enforce device restrictions if they exist
			if len(cfg.DeviceIDs) > 0 {
				for _, i := range cfg.DeviceIDs {
					if deviceListIndex == i {
						miningAllowed = true
					}
				}
			} else {
				miningAllowed = true
			}
			if miningAllowed {
				newDevice, err := NewDevice(deviceListIndex, deviceListEnabledCount, platformID, CLdeviceID, workDone)
				if err != nil {
					return nil, err
				}
				devices = append(devices, newDevice)
				deviceListEnabledCount++
			}
			deviceListIndex++
		}
	}
	return devices, nil
}

func getDeviceInfo(id cl.CL_device_id,
	name cl.CL_device_info,
	str string) string {

	var errNum cl.CL_int
	var paramValueSize cl.CL_size_t

	errNum = cl.CLGetDeviceInfo(id, name, 0, nil, &paramValueSize)

	if errNum != cl.CL_SUCCESS {
		return fmt.Sprintf("Failed to find OpenCL device info %s.\n", str)
	}

	var info interface{}
	errNum = cl.CLGetDeviceInfo(id, name, paramValueSize, &info, nil)
	if errNum != cl.CL_SUCCESS {
		return fmt.Sprintf("Failed to find OpenCL device info %s.\n", str)
	}

	switch name {
	case cl.CL_DEVICE_TYPE:
		var deviceTypeStr string

		appendBitfield(cl.CL_bitfield(info.(cl.CL_device_type)),
			cl.CL_bitfield(cl.CL_DEVICE_TYPE_CPU),
			DeviceTypeCPU,
			&deviceTypeStr)

		appendBitfield(cl.CL_bitfield(info.(cl.CL_device_type)),
			cl.CL_bitfield(cl.CL_DEVICE_TYPE_GPU),
			DeviceTypeGPU,
			&deviceTypeStr)

		info = deviceTypeStr
	}

	strinfo := fmt.Sprintf("%v", info)

	return strinfo
}

func (d *Device) Release() {
	cl.CLReleaseKernel(d.kernel)
	cl.CLReleaseProgram(d.program)
	cl.CLReleaseCommandQueue(d.queue)
	cl.CLReleaseMemObject(d.outputBuffer)
	cl.CLReleaseContext(d.context)
}
