// Copyright (c) 2016-2023 The Decred developers.

// Package util holds miscellaneous helper functions shared across monvark.
package util

import "fmt"

// Reverse reverses a byte array.
func Reverse(src []byte) []byte {
	dst := make([]byte, len(src))
	for i := len(src); i > 0; i-- {
		dst[len(src)-i] = src[i-1]
	}
	return dst
}

// RolloverExtraNonce rolls over the extraNonce if it goes over 0x00FFFFFF many
// hashes, since the first byte is reserved for the ID.
func RolloverExtraNonce(v *uint32) {
	if *v&0x00FFFFFF == 0x00FFFFFF {
		*v = *v & 0xFF000000
	} else {
		*v++
	}
}

// FormatHashRate sets the units properly when displaying a hashrate.
func FormatHashRate(h float64) string {
	const unit = 1000
	if h < unit {
		return fmt.Sprintf("%.0f h/s", h)
	}
	div, exp := float64(unit), 0
	for n := h / unit; n >= unit && exp < 6; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ch/s", h/div, "kMGTPEZ"[exp])
}
