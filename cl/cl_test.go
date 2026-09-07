// Copyright (c) 2026 The Decred developers.

package cl

import (
	"strings"
	"testing"
)

// TestLoadMissingLibraryReportsPath verifies that a library which cannot be
// resolved produces an error naming what was tried and pointing at the
// override, rather than a panic or a bare "not found".  This is the error most
// support reports will quote.
func TestLoadMissingLibraryReportsPath(t *testing.T) {
	const bogus = "/nonexistent/libOpenCL.so.1"

	err := Load(bogus)
	if err == nil {
		t.Fatal("expected an error for a library that does not exist")
	}
	if !strings.Contains(err.Error(), bogus) {
		t.Fatalf("error does not name the library that was tried: %v", err)
	}
	if !strings.Contains(err.Error(), "--opencl-lib") {
		t.Fatalf("error does not point at the override: %v", err)
	}
}

// TestErrorNameFallback checks that an unrecognised status still renders as
// something a user can quote.
func TestErrorNameFallback(t *testing.T) {
	if got := ErrorName(-30); got != "CL_INVALID_VALUE" {
		t.Fatalf("ErrorName(-30) = %q, want CL_INVALID_VALUE", got)
	}
	if got := ErrorName(-9999); !strings.Contains(got, "9999") {
		t.Fatalf("ErrorName(-9999) = %q, want the number in it", got)
	}
}
