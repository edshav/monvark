// Copyright (c) 2026 The Decred developers.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/edshav/monvark/cl"
	"github.com/edshav/monvark/work"
)

// TestDeviceHashAgreesWithHost runs the compiled kernel on every OpenCL device
// present and checks that each candidate they emit hashes, on the host, to a
// value whose final 32-bit word is zero.
//
// The kernel applies no target of its own -- it returns unless (v7 ^ v15) is
// zero -- so that is an invariant of every candidate rather than a property of
// this particular work item.  A miscompiled kernel, a wrong midstate or a
// byte-order slip breaks it immediately.
//
// Every device is exercised rather than a chosen one: the CPU and integrated
// devices run the same kernel through the same driver stack, so including them
// costs a few seconds of calibration and tests more hardware.
//
// The test skips where no OpenCL library or device is available, so it is a
// no-op on a CI runner and real on a developer machine.
func TestDeviceHashAgreesWithHost(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping GPU test in short mode")
	}
	if err := cl.Load(""); err != nil {
		t.Skipf("no OpenCL library available: %v", err)
	}

	// Benchmark mode gives each device a zero-valued work item and stops
	// foundCandidate from trying to submit anything.
	cfg = &config{Benchmark: true, AutocalibrateInts: []int{500}}

	// cl.Load already succeeded, so an OpenCL library is present: a failure
	// here is a real one -- e.g. clBuildProgram failing to compile the kernel
	// -- not an absent GPU, and must fail the test rather than skip it.
	devices, err := newMinerDevs(make(chan []byte, 10))
	if err != nil {
		t.Fatalf("could not open the OpenCL devices: %v", err)
	}
	if len(devices) == 0 {
		t.Skip("no OpenCL devices")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, len(devices))
	for _, d := range devices {
		device := d
		go func() {
			device.Run(ctx)
			done <- struct{}{}
		}()
		device.SetWork(ctx, &work.Work{})
	}

	// At the reference rig's ~2.1 s per candidate this is roughly seven from a
	// fast device; slower ones contribute fewer, and the assertion below only
	// needs the total to be non-zero.
	time.Sleep(15 * time.Second)
	cancel()
	for range devices {
		<-done
	}

	var candidates, mismatches uint64
	for _, d := range devices {
		d.Lock()
		candidates += d.allDiffOneShares
		mismatches += d.hashMismatches
		d.Unlock()
		d.Release()
	}

	if candidates == 0 {
		t.Fatalf("%d device(s) produced no candidates in 15s", len(devices))
	}
	if mismatches != 0 {
		t.Fatalf("%d of %d candidates hashed differently on the host than on "+
			"the GPU", mismatches, candidates)
	}
	t.Logf("%d device(s), %d candidates, all agreeing with the host",
		len(devices), candidates)
}
