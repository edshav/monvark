package main

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/edshav/monvark/work"
	chainjson "github.com/monetarium/monetarium-node/rpc/jsonrpc/types"
)

// block builds a verbose block result whose coinbase pays the given scripts.
func block(scripts ...string) *chainjson.GetBlockVerboseResult {
	vouts := make([]chainjson.Vout, 0, len(scripts))
	for i, s := range scripts {
		vouts = append(vouts, chainjson.Vout{
			N:            uint32(i),
			ScriptPubKey: chainjson.ScriptPubKeyResult{Hex: s},
		})
	}
	return &chainjson.GetBlockVerboseResult{
		RawTx: []chainjson.TxRawResult{{Vout: vouts}},
	}
}

func TestCoinbasePays(t *testing.T) {
	const ours = "76a914aabbccddeeff00112233445566778899aabbccdd88ac"
	const theirs = "76a914ffffffffffffffffffffffffffffffffffffffff88ac"

	tests := []struct {
		name       string
		blk        *chainjson.GetBlockVerboseResult
		wantScript string
		want       bool
	}{{
		name:       "single output, ours",
		blk:        block(ours),
		wantScript: ours,
		want:       true,
	}, {
		name:       "single output, somebody else's",
		blk:        block(theirs),
		wantScript: ours,
		want:       false,
	}, {
		// A coinbase carries the treasury and other outputs alongside the
		// miner's, so ours being one of several is the normal case.
		name:       "several outputs, one ours",
		blk:        block(theirs, ours, theirs),
		wantScript: ours,
		want:       true,
	}, {
		name:       "several outputs, none ours",
		blk:        block(theirs, theirs),
		wantScript: ours,
		want:       false,
	}, {
		name:       "no transactions at all",
		blk:        &chainjson.GetBlockVerboseResult{},
		wantScript: ours,
		want:       false,
	}, {
		name:       "coinbase with no outputs",
		blk:        block(),
		wantScript: ours,
		want:       false,
	}, {
		// The node renders script hex lower case today, but nothing in the
		// API promises it, and a case-sensitive compare would read as theft.
		name:       "ours, upper case hex",
		blk:        block("76A914AABBCCDDEEFF00112233445566778899AABBCCDD88AC"),
		wantScript: ours,
		want:       true,
	}, {
		// Empty want-script must never match, even if output is also empty.
		name:       "empty want-script, empty output",
		blk:        block(""),
		wantScript: "",
		want:       false,
	}, {
		// coinbasePays only inspects RawTx[0]; output in later transactions
		// should not match.
		name: "target in second transaction, not coinbase",
		blk: &chainjson.GetBlockVerboseResult{
			RawTx: []chainjson.TxRawResult{
				{Vout: []chainjson.Vout{{N: 0, ScriptPubKey: chainjson.ScriptPubKeyResult{Hex: theirs}}}},
				{Vout: []chainjson.Vout{{N: 0, ScriptPubKey: chainjson.ScriptPubKeyResult{Hex: ours}}}},
			},
		},
		wantScript: ours,
		want:       false,
	}}

	for _, test := range tests {
		if got := coinbasePays(test.blk, test.wantScript); got != test.want {
			t.Errorf("%s: coinbasePays = %v, want %v", test.name, got,
				test.want)
		}
	}
}

func TestWorkStale(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	bound := 15 * time.Minute

	fresh := uint32(now.Add(-1 * time.Minute).Unix())
	if workStale(fresh, now, bound) {
		t.Error("work one minute old reported stale")
	}

	// The bound is three times the five-minute block target, so a healthy
	// quiet chain trips it on roughly 5% of gaps and each trip is harmless.
	borderline := uint32(now.Add(-14 * time.Minute).Unix())
	if workStale(borderline, now, bound) {
		t.Error("work inside the bound reported stale")
	}

	old := uint32(now.Add(-16 * time.Minute).Unix())
	if !workStale(old, now, bound) {
		t.Error("work past the bound not reported stale")
	}

	// Work that has never been received is not stale: there is nothing to
	// have gone stale yet, and benchmark mode never sets it.
	if workStale(0, now, bound) {
		t.Error("unset work reported stale")
	}

	// bound itself must matter: the same work that is stale under the
	// 15-minute bound is not stale under a longer one.
	if workStale(old, now, 20*time.Minute) {
		t.Error("work reported stale under a longer bound")
	}
}

func TestSameTemplate(t *testing.T) {
	var current [192]byte
	for i := range current {
		current[i] = byte(i)
	}

	// The case that matters: getwork refreshes the timestamp on every call,
	// so a poll of an otherwise-unchanged template must still compare equal.
	t.Run("same template, different timestamp word", func(t *testing.T) {
		data := current
		binary.LittleEndian.PutUint32(data[128+4*work.TimestampWord:], 0xdeadbeef)
		if !sameTemplate(data[:], current) {
			t.Error("same template with a refreshed timestamp reported as a different one")
		}
	})

	t.Run("identical data", func(t *testing.T) {
		data := current
		if !sameTemplate(data[:], current) {
			t.Error("identical data reported as a different template")
		}
	})

	t.Run("different template", func(t *testing.T) {
		data := current
		data[0] ^= 0xff
		if sameTemplate(data[:], current) {
			t.Error("a changed template reported as the same one")
		}
	})

	t.Run("data shorter than the identity prefix", func(t *testing.T) {
		if sameTemplate(current[:100], current) {
			t.Error("short data reported as the same template")
		}
	})
}
