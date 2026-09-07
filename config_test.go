package main

import "testing"

// TestNormalizeAddress covers the forms sample-monvark.conf documents, plus the
// trailing-colon cases net.SplitHostPort accepts with an empty port.
func TestNormalizeAddress(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"", ":3333"},
		{":", ":3333"},
		{"127.0.0.1", "127.0.0.1:3333"},
		{"localhost:", "localhost:3333"},
		{"[::1]:", "[::1]:3333"},
		{":8337", ":8337"},
		{"127.0.0.1:8337", "127.0.0.1:8337"},
		{"[::1]:8337", "[::1]:8337"},
	}
	for _, test := range tests {
		if got := normalizeAddress(test.addr, "3333"); got != test.want {
			t.Errorf("normalizeAddress(%q) = %q, want %q", test.addr, got,
				test.want)
		}
	}
}
