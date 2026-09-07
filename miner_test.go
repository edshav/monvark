package main

import (
	"testing"

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
