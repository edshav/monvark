package main

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestMondConfOmitsPayoutKeys(t *testing.T) {
	got := mondConf("u", "p", "127.0.0.1:41287", "/h/rpc.cert", "/h/rpc.key")

	// The two keys that could redirect the payout must never be written to a
	// file.  miningaddr goes on the command line because the node picks
	// uniformly at random among its configured addresses and go-flags appends
	// rather than overrides, so an address in both places pays a random
	// fraction of blocks elsewhere.  That the other five fields interpolate is
	// not tested: it is one Sprintf with no branches.
	for _, forbidden := range []string{"miningaddr", "generate"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("mond.conf contains %q, which must stay on the command "+
				"line:\n%s", forbidden, got)
		}
	}
}

func TestCheckMondConf(t *testing.T) {
	dir := t.TempDir()

	// A file we wrote ourselves is fine.
	ours := filepath.Join(dir, "ours.conf")
	if err := os.WriteFile(ours, []byte(mondConf("u", "p", "a", "c", "k")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkMondConf(ours); err != nil {
		t.Fatalf("our own mond.conf rejected: %v", err)
	}

	// An absent file is fine: it is about to be written.
	if err := checkMondConf(filepath.Join(dir, "absent.conf")); err != nil {
		t.Fatalf("absent mond.conf rejected: %v", err)
	}

	// A file somebody added a payout-redirecting key to is not.  Overwriting
	// would in fact be safe, since our values win -- but silently repairing it
	// hides exactly the event worth reporting.
	for _, key := range []string{"miningaddr=Vs1", "generate=1"} {
		path := filepath.Join(dir, "tampered.conf")
		body := mondConf("u", "p", "a", "c", "k") + key + "\n"
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := checkMondConf(path); err == nil {
			t.Errorf("mond.conf containing %q accepted", key)
		}
	}

	// The permission refusal itself is checkPerms', tested in payout_test.go;
	// this only confirms mond.conf goes through it.
	if runtime.GOOS != "windows" {
		perm := filepath.Join(dir, "perm.conf")
		if err := os.WriteFile(perm, []byte(mondConf("u", "p", "a", "c", "k")), 0666); err != nil {
			t.Fatal(err)
		}
		if err := checkMondConf(perm); err == nil {
			t.Error("world-writable mond.conf accepted")
		}
	}
}

func TestMondArgs(t *testing.T) {
	cfg := &config{}
	args := mondArgs(cfg, "/h/mond.conf", "/h/node", "Vs1", false)

	want := []string{
		"--configfile=/h/mond.conf",
		"--appdata=/h/node",
		"--miningaddr=Vs1",
	}
	for _, w := range want {
		if !slices.Contains(args, w) {
			t.Errorf("args missing %q: %v", w, args)
		}
	}

	// --generate is never passed: the node's default is off, and a node with
	// CPU mining enabled refuses getwork.
	for _, arg := range args {
		if strings.HasPrefix(arg, "--generate") {
			t.Errorf("args contain %q", arg)
		}
	}

	// The P2P port is left alone when the default one is free, so the node
	// listens as usual and contributes fully to the network.
	for _, arg := range args {
		if strings.HasPrefix(arg, "--listen") {
			t.Errorf("args contain %q when the default P2P port is free", arg)
		}
	}

	// When it is busy -- because another node already holds it -- an ephemeral
	// port is taken instead.  Without this the child dies at startup with
	// "no valid listen address".
	busy := mondArgs(cfg, "/h/mond.conf", "/h/node", "Vs1", true)
	if !slices.Contains(busy, "--listen=:0") {
		t.Errorf("args missing --listen=:0 when the P2P port is busy: %v", busy)
	}

	testnet := mondArgs(&config{TestNet: true}, "/h/mond.conf", "/h/node", "Vs1", false)
	if !slices.Contains(testnet, "--testnet") {
		t.Errorf("args missing --testnet: %v", testnet)
	}
	simnet := mondArgs(&config{SimNet: true}, "/h/mond.conf", "/h/node", "Vs1", false)
	if !slices.Contains(simnet, "--simnet") {
		t.Errorf("args missing --simnet: %v", simnet)
	}

	// Mainnet has no DNS seed and no HTTP seeder, so without a bootstrap peer
	// the node loads an empty peers.json and never finds anybody: it reports
	// 0 peers forever rather than syncing.
	for _, p := range mainnetBootstrap {
		if !slices.Contains(args, "--addpeer="+p) {
			t.Errorf("args missing --addpeer=%s: %v", p, args)
		}
	}

	// A user-supplied list replaces the built-in one rather than adding to it,
	// which is the only way out when a built-in address goes dark.
	custom := mondArgs(&config{AddPeer: []string{"10.0.0.1:9508"}},
		"/h/mond.conf", "/h/node", "Vs1", false)
	if !slices.Contains(custom, "--addpeer=10.0.0.1:9508") {
		t.Errorf("args missing the supplied peer: %v", custom)
	}
	for _, p := range mainnetBootstrap {
		if slices.Contains(custom, "--addpeer="+p) {
			t.Errorf("args still carry built-in peer %s: %v", p, custom)
		}
	}

	// The built-in list is mainnet's.  Handing mainnet peers to a testnet node
	// would just log failed handshakes against the wrong network magic.
	for _, p := range mainnetBootstrap {
		if slices.Contains(testnet, "--addpeer="+p) {
			t.Errorf("testnet args carry mainnet peer %s: %v", p, testnet)
		}
	}
}

func TestMondPath(t *testing.T) {
	dir := t.TempDir()
	name := "mond"
	if runtime.GOOS == "windows" {
		name = "mond.exe"
	}
	beside := filepath.Join(dir, name)
	if err := os.WriteFile(beside, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}

	got, err := mondPath(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != beside {
		t.Errorf("mondPath = %q, want %q", got, beside)
	}

	// An override wins, for installations where the node does not sit beside
	// the miner.
	got, err = mondPath(dir, beside)
	if err != nil {
		t.Fatal(err)
	}
	if got != beside {
		t.Errorf("override ignored: %q", got)
	}

	// The error names the path searched, so the user knows where to put it.
	empty := t.TempDir()
	_, err = mondPath(empty, "")
	if err == nil {
		t.Fatal("missing node binary accepted")
	}
	if !strings.Contains(err.Error(), empty) {
		t.Errorf("error does not name the path searched: %v", err)
	}
}

func TestPortFree(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if portFree(net.JoinHostPort("127.0.0.1", port)) {
		t.Error("a port we are holding reported free")
	}

	ln.Close()
	if !portFree(net.JoinHostPort("127.0.0.1", port)) {
		t.Error("a released port reported busy")
	}
}
