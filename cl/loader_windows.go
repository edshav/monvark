// Copyright (c) 2026 The Decred developers.

//go:build windows

package cl

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

var libraryCandidates = []string{"OpenCL.dll"}

const (
	loadLibrarySearchSystem32 = 0x00000800
	loadWithAlteredSearchPath = 0x00000008
)

// openLibrary loads a DLL while keeping the application directory out of the
// search.  LoadLibraryW("OpenCL.dll") searches the application directory first,
// and this miner ships as an archive users extract into a download directory,
// so a co-extracted OpenCL.dll would otherwise be loaded into the process that
// holds the node's RPC credentials.
func openLibrary(path string) (uintptr, error) {
	flags := uintptr(loadLibrarySearchSystem32)
	if filepath.IsAbs(path) {
		// An absolute path can only have come from --opencl-lib, which is the
		// user's own explicit choice; restricting the search to System32 would
		// make it impossible to honour.
		flags = loadWithAlteredSearchPath
	}

	handle, err := windows.LoadLibraryEx(path, 0, flags)
	if err != nil {
		return 0, err
	}
	return uintptr(handle), nil
}
