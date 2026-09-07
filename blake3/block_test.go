// Copyright (c) 2026 The Decred developers.

package blake3

import (
	"bytes"
	"math/rand"
	"testing"

	ref "lukechampine.com/blake3"
)

// hashWork reproduces the way the miner hashes a work item: the first 128 bytes
// are folded into a midstate and the 52-byte tail is hashed as the final block.
// This mirrors device.go's updateCurrentWork and foundCandidate.
func hashWork(data *[192]byte) [32]byte {
	midstate := Block(IV, data[0:64], FlagChunkStart)
	midstate = Block(midstate, data[64:128], 0)
	return FinalBlock(midstate, data[128:180])
}

// TestFinalBlockMatchesReference verifies the midstate-based BLAKE3 in this
// package against an independent implementation.  The 180 bytes of a block
// header fit in a single BLAKE3 chunk, which is therefore also the root, so the
// midstate construction must agree with a plain BLAKE3-256 over the same bytes.
func TestFinalBlockMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 256; i++ {
		var data [192]byte
		rng.Read(data[:])

		if got, want := hashWork(&data), ref.Sum256(data[:180]); !bytes.Equal(got[:], want[:]) {
			t.Fatalf("random vector %d mismatch:\n data %x\n got  %x\n want %x",
				i, data[:180], got, want)
		}
	}
}
