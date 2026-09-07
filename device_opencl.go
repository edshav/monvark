// Copyright (c) 2016-2023 The Decred developers.

package main

import (
	"context"
	_ "embed"
	"fmt"
	"math"
	"os"
	"slices"
	"sync"
	"time"
	"unsafe"

	"github.com/edshav/monvark/cl"
	"github.com/edshav/monvark/util"
	"github.com/edshav/monvark/work"
)

// kernelSource is the OpenCL kernel, compiled into the binary.  --kernel used
// to point at this file on disk and defaulted to a bare relative path, so the
// miner only ran from its own build directory.
//
//go:embed blake3.cl
var kernelSource string

// gpuLib is the GPU library in use, reported at startup and by --version.
const gpuLib = "OpenCL"

const (
	outputBufferSize = uint64(64)
	localWorksize    = 64
	uint32Size       = uint64(unsafe.Sizeof(uint32(0)))
)

var zeroSlice = []uint32{0}

func clError(status int32, f string) error {
	return fmt.Errorf("%s returned error %s (%d)", f, cl.ErrorName(status), status)
}

// deviceName returns a device's CL_DEVICE_NAME.
func deviceName(id cl.DeviceID) string {
	name, status := cl.DeviceInfoString(id, cl.DeviceName)
	if status != cl.Success {
		return fmt.Sprintf("<unknown: %v>", clError(status, "clGetDeviceInfo"))
	}
	return name
}

type Device struct {
	sync.Mutex
	index int

	// Items for OpenCL device
	deviceID     cl.DeviceID
	deviceName   string
	context      cl.Context
	queue        cl.Queue
	outputBuffer cl.Mem
	program      cl.Program
	kernel       cl.Kernel

	workSize uint32

	// dutyCycle is the percentage of the time this device hashes; the rest it
	// idles.  100 is flat out.
	dutyCycle int

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

	// hashMismatches counts candidates whose host-recomputed hash did not end
	// in a zero word, which means the host and the GPU disagree.
	hashMismatches uint64
}

// getCLPlatforms returns the OpenCL platforms present, which is none at all on
// a machine whose ICD loader is installed but has no vendor ICD behind it --
// common on Linux, where ocl-icd-libopencl1 arrives as a dependency of
// unrelated packages.  That case is an empty result rather than an error, so
// the caller ends at "no devices started" instead of an opaque OpenCL status.
func getCLPlatforms() ([]cl.PlatformID, error) {
	var numPlatforms uint32
	status := cl.GetPlatformIDs(0, nil, &numPlatforms)
	if status == cl.PlatformNotFound || (status == cl.Success && numPlatforms == 0) {
		return nil, nil
	}
	if status != cl.Success {
		return nil, clError(status, "clGetPlatformIDs")
	}

	platforms := make([]cl.PlatformID, numPlatforms)
	if status := cl.GetPlatformIDs(numPlatforms, platforms, nil); status != cl.Success {
		return nil, clError(status, "clGetPlatformIDs")
	}
	return platforms, nil
}

// getCLDevices returns the list of devices for the given platform.
func getCLDevices(platform cl.PlatformID) ([]cl.DeviceID, error) {
	var numDevices uint32
	status := cl.GetDeviceIDs(platform, cl.DeviceTypeAll, 0, nil, &numDevices)
	if status != cl.Success && status != cl.DeviceNotFound {
		return nil, clError(status, "clGetDeviceIDs")
	}
	if numDevices == 0 {
		return nil, nil
	}

	devices := make([]cl.DeviceID, numDevices)
	status = cl.GetDeviceIDs(platform, cl.DeviceTypeAll, numDevices, devices, nil)
	if status != cl.Success {
		return nil, clError(status, "clGetDeviceIDs")
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
			fmt.Printf("DEV #%d: %s\n", deviceListIndex, deviceName(deviceID))
			deviceListIndex++
		}

	}
}

func NewDevice(index int, order int, deviceID cl.DeviceID,
	workDone chan []byte) (*Device, error) {
	d := &Device{
		index:      index,
		deviceID:   deviceID,
		deviceName: deviceName(deviceID),
		newWork:    make(chan *work.Work, 5),
		workDone:   workDone,
	}

	var status int32

	// Create the CL context.
	d.context = cl.CreateContext(nil, 1, []cl.DeviceID{deviceID}, 0, 0, &status)
	if status != cl.Success {
		return nil, clError(status, "clCreateContext")
	}

	// Create the command queue.
	d.queue = cl.CreateCommandQueue(d.context, deviceID, 0, &status)
	if status != cl.Success {
		return nil, clError(status, "clCreateCommandQueue")
	}

	// Create the output buffer.
	d.outputBuffer = cl.CreateBuffer(d.context, cl.MemReadWrite,
		uint32Size*outputBufferSize, nil, &status)
	if status != cl.Success {
		return nil, clError(status, "clCreateBuffer")
	}

	// Create the program from the embedded kernel source.
	d.program = cl.CreateProgramWithSource(d.context, kernelSource, &status)
	if status != cl.Success {
		return nil, clError(status, "clCreateProgramWithSource")
	}

	// Build the program for the device.
	options := fmt.Sprintf("-D WORKSIZE=%d", localWorksize)
	status = cl.BuildProgram(d.program, 1, []cl.DeviceID{deviceID}, options, 0, 0)
	if status != cl.Success {
		err := clError(status, "clBuildProgram")
		if buildLog, s := cl.ProgramBuildLog(d.program, deviceID); s == cl.Success {
			minrLog.Errorf("Kernel build log:\n%s", buildLog)
		}
		return nil, err
	}

	// Create the kernel.
	d.kernel = cl.CreateKernel(d.program, "search", &status)
	if status != cl.Success {
		return nil, clError(status, "clCreateKernel")
	}

	d.started = uint32(time.Now().Unix())

	// The duty cycle takes the same per-device form as the settings below: one
	// global value, or one entry per device.  An unset slice means the default
	// rather than a panic, so a config assembled by hand -- a test's -- does
	// not have to fill in every tuning knob.
	d.dutyCycle = defaultDutyCycle
	if len(cfg.DutyCycleInts) > 0 {
		d.dutyCycle = cfg.DutyCycleInts[0]
		if order < len(cfg.DutyCycleInts) {
			d.dutyCycle = cfg.DutyCycleInts[order]
		}
	}

	// Autocalibrate the desired work size for the kernel, or use one of the
	// values passed explicitly by the use.
	// The intensity or worksize must be set by the user.
	userSetWorkSize := len(cfg.IntensityInts) > 0 || len(cfg.WorkSizeInts) > 0

	// In each of the three settings below the first value applies to every
	// device, and a value at this device's own position overrides it.
	var globalWorkSize uint32
	if !userSetWorkSize {
		calibrateTime := cfg.AutocalibrateInts[0]
		if order < len(cfg.AutocalibrateInts) {
			calibrateTime = cfg.AutocalibrateInts[order]
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
			intensity := cfg.IntensityInts[0]
			if order < len(cfg.IntensityInts) {
				intensity = cfg.IntensityInts[order]
			}
			globalWorkSize = 1 << uint32(intensity)
		}
		if len(cfg.WorkSizeInts) > 0 {
			globalWorkSize = cfg.WorkSizeInts[0]
			if order < len(cfg.WorkSizeInts) {
				globalWorkSize = cfg.WorkSizeInts[order]
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
	// Name the throttle when there is one.  A duty cycle that silently failed
	// to apply -- a typo in the config key, say -- otherwise looks exactly like
	// one that worked, since the hash rate is a cumulative average that takes
	// minutes to mean anything.
	throttle := ""
	if d.dutyCycle < 100 {
		throttle = fmt.Sprintf(" at a %d%% duty cycle", d.dutyCycle)
	}
	minrLog.Infof("Started DEV #%d: %s%s", d.index, d.deviceName, throttle)
	outputData := make([]uint32, outputBufferSize)

	// Initialize the nonces for the device such that each device in the same
	// system is doing different work while also helping prevent collisions
	// across multiple processes and systems working on the same template.
	if err := d.initNonces(); err != nil {
		return err
	}

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

		if err := d.setKernelArgs(); err != nil {
			return err
		}

		elapsedTime, err := d.runKernel(d.workSize, outputData)
		if err != nil {
			return err
		}

		// The kernel increments its candidate counter without bounding it
		// against the buffer, and the kernel is frozen, so clamp the count
		// here rather than let a garbage value index out of range.
		count := min(outputData[0], uint32(outputBufferSize)-1)
		for i := uint32(0); i < count; i++ {
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

		minrLog.Tracef("DEV #%d: Kernel execution to read time: %v", d.index,
			elapsedTime)

		// Idle out the rest of the duty cycle window, after the candidates
		// have been dealt with rather than before: a found block goes to the
		// node at once, whatever the throttle is set to.
		if idle := idleFor(elapsedTime, d.dutyCycle); idle > 0 {
			select {
			case <-ctxDoneCh:
				return nil
			case <-time.After(idle):
			}
		}
	}
}

// setKernelArgs sets every argument of the mining kernel from the device's
// current midstate and last block.  The work size calibration launches the same
// kernel with the same arguments.
func (d *Device) setKernelArgs() error {
	// arg 0: pointer to the buffer
	obuf := d.outputBuffer
	status := cl.SetKernelArg(d.kernel, 0, uint64(unsafe.Sizeof(obuf)),
		unsafe.Pointer(&obuf))
	if status != cl.Success {
		return clError(status, "clSetKernelArg")
	}

	// args 1..8: midstate
	for i := 0; i < 8; i++ {
		ms := d.midstate[i]
		status = cl.SetKernelArg(d.kernel, uint32(i+1), uint32Size,
			unsafe.Pointer(&ms))
		if status != cl.Success {
			return clError(status, "clSetKernelArg")
		}
	}

	// args 9..20: lastBlock except nonce
	i2 := 0
	for i := 0; i < 12; i++ {
		if i2 == work.Nonce0Word {
			i2++
		}
		lb := d.lastBlock[i2]
		status = cl.SetKernelArg(d.kernel, uint32(i+9), uint32Size,
			unsafe.Pointer(&lb))
		if status != cl.Success {
			return clError(status, "clSetKernelArg")
		}
		i2++
	}

	return nil
}

// runKernel clears the candidate count, launches one kernel over
// globalWorkSize work items and reads the output buffer back into out.  It
// returns how long the launch and the blocking read took.
func (d *Device) runKernel(globalWorkSize uint32, out []uint32) (time.Duration,
	error) {
	// Clear the found count from the buffer
	status := cl.EnqueueWriteBuffer(d.queue, d.outputBuffer, false, 0,
		uint32Size, unsafe.Pointer(&zeroSlice[0]))
	if status != cl.Success {
		return 0, clError(status, "clEnqueueWriteBuffer")
	}

	// Execute the kernel and follow its execution time.
	currentTime := time.Now()
	status = cl.EnqueueNDRangeKernel(d.queue, d.kernel, uint64(globalWorkSize),
		localWorksize)
	if status != cl.Success {
		return 0, clError(status, "clEnqueueNDRangeKernel")
	}

	// Read the output buffer.
	status = cl.EnqueueReadBuffer(d.queue, d.outputBuffer, true, 0,
		uint32Size*outputBufferSize, unsafe.Pointer(&out[0]))
	if status != cl.Success {
		return 0, clError(status, "clEnqueueReadBuffer")
	}

	return time.Since(currentTime), nil
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
			// Enforce device restrictions if they exist
			if len(cfg.DeviceIDs) == 0 || slices.Contains(cfg.DeviceIDs, deviceListIndex) {
				newDevice, err := NewDevice(deviceListIndex, deviceListEnabledCount, CLdeviceID, workDone)
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

func (d *Device) Release() {
	cl.ReleaseKernel(d.kernel)
	cl.ReleaseProgram(d.program)
	cl.ReleaseCommandQueue(d.queue)
	cl.ReleaseMemObject(d.outputBuffer)
	cl.ReleaseContext(d.context)
}
