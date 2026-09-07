// Copyright (c) 2016 The Decred developers.

// Package work defines the mining work item shared between work sources and
// devices.
package work

import (
	"math/big"
)

// These are the locations of various data inside Work.Data.
const (
	TimestampWord = 2
	Nonce0Word    = 3
	Nonce1Word    = 4
	Nonce2Word    = 5
	Nonce3Word    = 6
)

// Work holds the data returned from getwork.
type Work struct {
	Data         [192]byte
	Target       *big.Int
	JobTime      uint32
	TimeReceived uint32
}
