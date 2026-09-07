// Copyright (c) 2026 The Decred developers.

//go:build !windows

package cl

import "github.com/ebitengine/purego"

// libraryCandidates covers both Unix platforms in one list: a Linux soname
// simply fails to load on macOS and the loop moves on.
//
// Bare sonames are correct here.  dlopen does not search the working directory,
// so the library-planting vector that shapes the Windows loader does not exist
// on Unix, and absolute paths would only add per-distribution failure modes.
var libraryCandidates = []string{
	"libOpenCL.so.1",
	"libOpenCL.so",
	"/System/Library/Frameworks/OpenCL.framework/OpenCL",
}

func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
}
