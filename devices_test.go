package main

import (
	"slices"
	"testing"
)

func TestParseDeviceChoice(t *testing.T) {
	const numDevices = 3

	tests := []struct {
		name string
		line string
		want []int
	}{{
		name: "single device",
		line: "2\n",
		want: []int{2},
	}, {
		name: "several devices, spaced the way a person types them",
		line: "0, 2\n",
		want: []int{0, 2},
	}, {
		name: "carriage return from a windows terminal",
		line: "1\r\n",
		want: []int{1},
	}, {
		// Silence must not mean "take every device": that is the outcome the
		// question exists to prevent.
		name: "empty answer is not an answer",
		line: "\n",
	}, {
		name: "blank answer is not an answer",
		line: "   \n",
	}, {
		name: "index past the end",
		line: "3",
	}, {
		name: "negative index",
		line: "-1",
	}, {
		name: "not a number",
		line: "gpu",
	}}

	for _, test := range tests {
		got, err := parseDeviceChoice(test.line, numDevices)
		if test.want == nil {
			if err == nil {
				t.Errorf("%s: %q accepted as %v", test.name, test.line, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %q rejected: %v", test.name, test.line, err)
			continue
		}
		if !slices.Equal(got, test.want) {
			t.Errorf("%s: got %v, want %v", test.name, got, test.want)
		}
	}
}
