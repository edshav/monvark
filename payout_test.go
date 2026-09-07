package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/monetarium/monetarium-node/chaincfg"
)

func TestPayoutAddress(t *testing.T) {
	const file = "/home/u/.monvark/monvark.conf"

	tests := []struct {
		name       string
		effective  string
		fromFile   string
		wantAddr   string
		wantPrompt bool
		wantErr    bool
	}{{
		name:       "neither source supplies one",
		effective:  "",
		fromFile:   "",
		wantPrompt: true,
	}, {
		name:      "flag only",
		effective: "Vs1",
		fromFile:  "",
		wantAddr:  "Vs1",
	}, {
		// Also the case where the flag repeats what the file already says:
		// after go-flags has merged them the two are indistinguishable.
		name:      "file supplies it",
		effective: "Vs1",
		fromFile:  "Vs1",
		wantAddr:  "Vs1",
	}, {
		name:      "flag disagrees with file",
		effective: "Vs2",
		fromFile:  "Vs1",
		wantErr:   true,
	}}

	for _, test := range tests {
		addr, prompt, err := payoutAddress(test.effective, test.fromFile, file)
		if test.wantErr {
			if err == nil {
				t.Errorf("%s: want error, got none", test.name)
				continue
			}
			if !strings.Contains(err.Error(), file) {
				t.Errorf("%s: error does not name the config file: %v",
					test.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", test.name, err)
			continue
		}
		if prompt != test.wantPrompt {
			t.Errorf("%s: prompt = %v, want %v", test.name, prompt,
				test.wantPrompt)
		}
		if addr != test.wantAddr {
			t.Errorf("%s: addr = %q, want %q", test.name, addr, test.wantAddr)
		}
	}
}

func TestValidatePayoutRejectsWrongNetwork(t *testing.T) {
	// A simnet address supplied while mainnet is active must be rejected in
	// the miner's own words rather than surfacing later as the node exiting.
	const simnetAddr = "SsUSbmtvi1BrrMjbZXju1DL825SQLKFAeCv"

	if err := validatePayout(simnetAddr, chaincfg.SimNetParams()); err != nil {
		t.Fatalf("simnet address rejected on simnet: %v", err)
	}
	if err := validatePayout(simnetAddr, chaincfg.MainNetParams()); err == nil {
		t.Fatal("simnet address accepted on mainnet")
	}
	if err := validatePayout("not-an-address", chaincfg.MainNetParams()); err == nil {
		t.Fatal("garbage accepted as an address")
	}
	if err := validatePayout("", chaincfg.MainNetParams()); err == nil {
		t.Fatal("empty string accepted as an address")
	}
}

func TestCheckPerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are synthesized on Windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "monvark.conf")
	if err := os.WriteFile(path, []byte("miningaddr=Vs1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkPerms(path); err != nil {
		t.Fatalf("0600 rejected: %v", err)
	}

	// Write access to this file is write access to the payout, so anything
	// wider than the owner is refused.
	if err := os.Chmod(path, 0620); err != nil {
		t.Fatal(err)
	}
	if err := checkPerms(path); err == nil {
		t.Fatal("group-writable config accepted")
	}
	if err := os.Chmod(path, 0602); err != nil {
		t.Fatal(err)
	}
	if err := checkPerms(path); err == nil {
		t.Fatal("world-writable config accepted")
	}

	// A file that does not exist yet is not an error: first run creates it.
	if err := checkPerms(filepath.Join(dir, "absent.conf")); err != nil {
		t.Fatalf("absent config rejected: %v", err)
	}
}

func TestAppendMiningAddr(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monvark.conf")

	if err := appendMiningAddr(path, "Vs1"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("created with mode %v, want 0600", perm)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "miningaddr=Vs1") {
		t.Fatalf("address not written: %q", got)
	}

	// Appending to a file that already has content preserves it.
	if err := os.WriteFile(path, []byte("debuglevel=debug\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := appendMiningAddr(path, "Vs2"); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "debuglevel=debug") {
		t.Fatalf("existing content lost: %q", got)
	}
	if !strings.Contains(string(got), "miningaddr=Vs2") {
		t.Fatalf("address not appended: %q", got)
	}
}
