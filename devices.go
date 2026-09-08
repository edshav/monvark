package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// parseDeviceChoice turns an answer to the device prompt into device indexes.
// The answer takes the comma separated form the --devices flag already uses, so
// the question and the flag are the same language.
//
// An empty answer is rejected rather than defaulting to every device: this
// question exists because taking every device can freeze the machine, and
// silence must not be the way to ask for that.
func parseDeviceChoice(line string, numDevices int) ([]int, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, errors.New("no devices chosen")
	}

	ids, err := parseInts("device", line)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if id < 0 || id >= numDevices {
			return nil, fmt.Errorf("there is no device %d; the devices are "+
				"0 to %d", id, numDevices-1)
		}
	}
	return ids, nil
}

// promptDevices lists the devices on stdout and reads a choice from stdin.
func promptDevices(names []string) ([]int, error) {
	fmt.Fprintf(os.Stdout, "\nFound %d OpenCL devices:\n\n", len(names))
	for i, name := range names {
		fmt.Fprintf(os.Stdout, "  %d  %s\n", i, name)
	}

	line, err := promptLine("\nMining takes a device whole, and a kernel " +
		"cannot be preempted: a device\nthat draws a screen stops repainting " +
		"while one runs.  Name the ones to\nmine with -- on a machine you sit " +
		"at, that is the discrete card only.\n\nDevices, comma separated: ")
	if err != nil {
		return nil, err
	}
	return parseDeviceChoice(line, len(names))
}

// resolveDevices fills cfg.DeviceIDs by asking, on first run, which devices to
// mine with.  It reports whether it asked; saving the answer is left to
// saveDeviceChoice, once the chosen devices have been built.
//
// It asks only where there is something to choose and somebody to ask.
func resolveDevices(cfg *config) (bool, error) {
	// An explicit choice, from the flag or the config file, is the answer.
	if len(cfg.DeviceIDs) > 0 {
		return false, nil
	}

	names, err := listDevices()
	if err != nil {
		return false, err
	}
	// No terminal, no question and no warning: under systemd, a docker run
	// without -i, or a redirected run there is nobody for either -- and every
	// device is the right answer on a rig nobody sits at, so take it rather
	// than failing the way a missing payout address does.
	if !interactive() {
		return false, nil
	}

	// Nothing to choose between.  Saving a choice here would pin an index on a
	// machine that has no alternative to it, which is how a second card added
	// later ends up silently ignored -- so say what is about to happen instead
	// of asking a question with one answer.
	//
	// This goes to stdout rather than the log because it is the whole of what
	// this machine is told, and debuglevel is a documented setting: a laptop
	// user who quietened the miner would otherwise lose the one warning that
	// their screen is about to stop repainting.
	if len(names) < 2 {
		if len(names) == 1 {
			fmt.Fprintf(os.Stdout, "\nMining takes a device whole and a "+
				"kernel cannot be preempted: if\n%s draws your screen, it "+
				"stops repainting while monvark runs.\n", names[0])
		}
		return false, nil
	}

	// The answer will be written here later, so refuse an unsafe file before
	// asking rather than after.
	if err := checkPerms(cfg.ConfigFile, permsWritable); err != nil {
		return false, err
	}

	ids, err := promptDevices(names)
	if err != nil {
		return false, err
	}
	cfg.DeviceIDs = ids
	return true, nil
}

// saveDeviceChoice writes the answered device list to the config file, naming
// the devices in a comment above it -- an index on its own says nothing once
// the hardware has changed under it, and this file is meant to be read by hand.
//
// It runs only once newMinerDevs has built every chosen device.  A choice
// written before that is a choice that outlives the failure it caused: the
// device that would not compile blake3.cl is still in the file on the next run,
// which no longer asks, fails the same way, and never points at the saved line.
func saveDeviceChoice(cfg *config, devices []*Device) error {
	var setting strings.Builder
	ids := make([]string, len(devices))
	for i, d := range devices {
		fmt.Fprintf(&setting, "; %s\n", d.deviceName)
		ids[i] = strconv.Itoa(d.index)
	}
	fmt.Fprintf(&setting, "devices=%s\n", strings.Join(ids, ","))

	if err := appendSetting(cfg.ConfigFile, setting.String()); err != nil {
		return err
	}
	mainLog.Infof("Device choice saved to %s", cfg.ConfigFile)
	return nil
}
