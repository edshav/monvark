// Copyright (c) 2026 The Decred developers.

package main

import (
	"strings"
	"testing"
)

// TestKernelSourceEmbedded ensures the OpenCL kernel is compiled into the
// binary.  The miner is distributed as a bare executable, so a kernel that has
// to be found on disk at run time fails to start on every machine except the
// one it was built on.
func TestKernelSourceEmbedded(t *testing.T) {
	if len(kernelSource) == 0 {
		t.Fatal("kernel source is empty; the go:embed directive is not working")
	}
	if !strings.Contains(kernelSource, "__kernel void\nsearch(") {
		t.Fatalf("embedded source does not declare the search kernel (%d bytes)",
			len(kernelSource))
	}
}
