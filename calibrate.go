// Copyright (c) 2016-2023 The Decred developers.

package main

import (
	"math"
	"time"

	"github.com/edshav/monvark/work"
)

// getKernelExecutionTime returns the kernel execution time for a device.
func (d *Device) getKernelExecutionTime(globalWorksize uint32) (time.Duration,
	error) {
	d.work = work.Work{}

	minrLog.Tracef("Started DEV #%d: %s for kernel execution time fetch",
		d.index, d.deviceName)
	outputData := make([]uint32, outputBufferSize)

	if err := d.setKernelArgs(); err != nil {
		return 0, err
	}

	elapsedTime, err := d.runKernel(globalWorksize, outputData)
	if err != nil {
		return 0, err
	}

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
