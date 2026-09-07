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
		name string
		blk  *chainjson.GetBlockVerboseResult
		want bool
	}{{
		name: "single output, ours",
		blk:  block(ours),
		want: true,
	}, {
		name: "single output, somebody else's",
		blk:  block(theirs),
		want: false,
	}, {
		// A coinbase carries the treasury and other outputs alongside the
		// miner's, so ours being one of several is the normal case.
		name: "several outputs, one ours",
		blk:  block(theirs, ours, theirs),
		want: true,
	}, {
		name: "several outputs, none ours",
		blk:  block(theirs, theirs),
		want: false,
	}, {
		name: "no transactions at all",
		blk:  &chainjson.GetBlockVerboseResult{},
		want: false,
	}, {
		name: "coinbase with no outputs",
		blk:  block(),
		want: false,
	}, {
		// The node renders script hex lower case today, but nothing in the
		// API promises it, and a case-sensitive compare would read as theft.
		name: "ours, upper case hex",
		blk:  block("76A914AABBCCDDEEFF00112233445566778899AABBCCDD88AC"),
		want: true,
	}}

	for _, test := range tests {
		if got := coinbasePays(test.blk, ours); got != test.want {
			t.Errorf("%s: coinbasePays = %v, want %v", test.name, got,
				test.want)
		}
	}
}
