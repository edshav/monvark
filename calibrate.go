// Copyright (c) 2016-2023 The Decred developers.

package main

import (
	"math"
	"time"
	"unsafe"

	"github.com/edshav/monvark/cl"
	"github.com/edshav/monvark/work"
)

// getKernelExecutionTime returns the kernel execution time for a device.
func (d *Device) getKernelExecutionTime(globalWorksize uint32) (time.Duration,
	error) {
	d.work = work.Work{}

	minrLog.Tracef("Started DEV #%d: %s for kernel execution time fetch",
		d.index, d.deviceName)
	outputData := make([]uint32, outputBufferSize)

	var status int32

	// arg 0: pointer to the buffer
	obuf := d.outputBuffer
	status = cl.SetKernelArg(d.kernel, 0, uint64(unsafe.Sizeof(obuf)),
		unsafe.Pointer(&obuf))
	if status != cl.Success {
		return time.Duration(0), clError(status, "clSetKernelArg")
	}

	// args 1..8: midstate
	for i := 0; i < 8; i++ {
		ms := d.midstate[i]
		status = cl.SetKernelArg(d.kernel, uint32(i+1), uint32Size,
			unsafe.Pointer(&ms))
		if status != cl.Success {
			return time.Duration(0), clError(status, "clSetKernelArg")
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
			return time.Duration(0), clError(status, "clSetKernelArg")
		}
		i2++
	}

	// Clear the found count from the buffer
	status = cl.EnqueueWriteBuffer(d.queue, d.outputBuffer, false, 0, uint32Size,
		unsafe.Pointer(&zeroSlice[0]))
	if status != cl.Success {
		return time.Duration(0), clError(status, "clEnqueueWriteBuffer")
	}

	// Execute the kernel and follow its execution time.
	currentTime := time.Now()
	status = cl.EnqueueNDRangeKernel(d.queue, d.kernel, uint64(globalWorksize),
		localWorksize)
	if status != cl.Success {
		return time.Duration(0), clError(status, "clEnqueueNDRangeKernel")
	}

	// Read the output buffer.
	status = cl.EnqueueReadBuffer(d.queue, d.outputBuffer, true, 0,
		uint32Size*outputBufferSize, unsafe.Pointer(&outputData[0]))
	if status != cl.Success {
		return time.Duration(0), clError(status, "clEnqueueReadBuffer")
	}

	elapsedTime := time.Since(currentTime)
	minrLog.Tracef("DEV #%d: Kernel execution to read time for work "+
		"size calibration: %v", d.index, elapsedTime)

	return elapsedTime, nil
}

// calcWorkSizeForMilliseconds calculates the correct worksize to achieve
// a device execution cycle of the passed duration in milliseconds.
func (d *Device) calcWorkSizeForMilliseconds(ms int) (uint32, error) {
	workSize := uint32(1 << 10)
	timeToAchieve := time.Duration(ms) * time.Millisecond
	for {
		execTime, err := d.getKernelExecutionTime(workSize)
		if err != nil {
			return 0, err
		}

		// If we fail to go above the desired execution time, double
		// the work size and try again.
		if execTime < timeToAchieve && workSize < 1<<30 {
			workSize <<= 1
			continue
		}

		// We're passed the desired execution time, so now calculate
		// what the ideal work size should be.
		adj := float64(workSize) * (float64(timeToAchieve) / float64(execTime))
		adj /= 256.0
		adjMultiple256 := uint32(math.Ceil(adj))
		workSize = adjMultiple256 * 256

		break
	}

	return workSize, nil
}
