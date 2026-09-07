package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/txscript/stdaddr"
	"golang.org/x/term"
)

// payoutAddress reports which payout address to use.  effective is the value
// after the command line has overridden the config file; fromFile is the config
// file's own value, captured before that override.  An empty result means
// neither source supplied one and the user has to be asked.
//
// A command line address that disagrees with the file is an error naming the
// file.  The file is never rewritten to match and never silently overwritten:
// that is the install-versus-upgrade distinction, and getting it wrong is the
// bug that ships when a user re-extracts the archive over an existing install.
func payoutAddress(effective, fromFile, filePath string) (string, error) {
	if effective == "" {
		return "", nil
	}
	if fromFile != "" && effective != fromFile {
		return "", fmt.Errorf("the payout address on the command "+
			"line (%s) disagrees with the one in %s (%s); remove one of "+
			"them, this file is never overwritten", effective, filePath,
			fromFile)
	}
	return effective, nil
}

// validatePayout reports whether addr is a valid address on params.  It is
// called before anything is written, so a typo or a testnet address supplied on
// mainnet is reported in the miner's own words rather than surfacing later as
// the node exiting during a progress display.
func validatePayout(addr string, params *chaincfg.Params) error {
	if addr == "" {
		return errors.New("no payout address")
	}
	if _, err := stdaddr.DecodeAddress(addr, params); err != nil {
		return fmt.Errorf("%q is not a valid %s address: %w", addr,
			params.Name, err)
	}
	return nil
}

// checkPerms fails when path is readable or writable by anyone but its owner.
// Both files monvark writes go through it: every field in monvark.conf parses
// back into the config struct including the payout address, and mond.conf
// carries the node's credentials -- so access to either is access to the
// payout.  The mask is 0077 rather than 0022 deliberately: merely reading
// mond.conf is enough to drive the node's RPC.  A file that does not exist yet
// is not an error: first run creates it.
//
// Windows is skipped because Go synthesizes the unix mode bits there, so the
// check would reject files that are in fact fine.
func checkPerms(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	if perm := info.Mode().Perm(); perm&0077 != 0 {
		return fmt.Errorf("%s is accessible to other users (mode %04o); "+
			"run: chmod 600 %s", path, perm, path)
	}
	return nil
}

// appendMiningAddr appends the payout address to path, creating it 0600 if it
// does not exist and preserving whatever is already there.  A file whose last
// line has no terminating newline gets one first: monvark's own writes always
// end in one, but this file is meant to be hand-edited, and without the check
// the address would be glued onto the end of whatever key came last.
func appendMiningAddr(path, addr string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	prefix := ""
	if size := info.Size(); size > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], size-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			prefix = "\n"
		}
	}

	_, err = fmt.Fprintf(f, "%sminingaddr=%s\n", prefix, addr)
	return err
}

// promptPayout asks for an address on stdout and reads one from stdin.
func promptPayout() (string, error) {
	fmt.Fprint(os.Stdout, "\nNo payout address configured.\n"+
		"Block rewards are paid to a Monetarium address you control.  Create\n"+
		"one with monetarium-wallet and paste it below.  The wallet is not\n"+
		"needed while mining and should not be installed on this machine;\n"+
		"monvark never holds keys.\n\nPayout address: ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	// TrimSpace also removes the carriage return Windows terminals supply.
	return strings.TrimSpace(line), nil
}

// resolvePayout returns the address block rewards are paid to, asking the user
// for one on first run.  Precedence is the command line, then the config file,
// then the prompt.
func resolvePayout(cfg *config) (string, error) {
	if err := checkPerms(cfg.ConfigFile); err != nil {
		return "", err
	}

	addr, err := payoutAddress(cfg.MiningAddr, cfg.fileMiningAddr,
		cfg.ConfigFile)
	if err != nil {
		return "", err
	}

	if addr != "" {
		if err := validatePayout(addr, chainParams); err != nil {
			return "", err
		}
		mainLog.Infof("Paying block rewards to %s", addr)
		return addr, nil
	}

	// Only a terminal can answer a question.  Under systemd (StandardInput=null
	// by default) or a docker run without -i there is nobody to ask, so say
	// what to do instead of blocking on a stdin that will never answer.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("no payout address configured and stdin is not "+
			"a terminal; pass --miningaddr, or add miningaddr= to %s",
			cfg.ConfigFile)
	}

	addr, err = promptPayout()
	if err != nil {
		return "", err
	}
	// Validate before writing anything.
	if err := validatePayout(addr, chainParams); err != nil {
		return "", err
	}
	if err := appendMiningAddr(cfg.ConfigFile, addr); err != nil {
		return "", err
	}
	mainLog.Infof("Payout address saved to %s", cfg.ConfigFile)
	return addr, nil
}
