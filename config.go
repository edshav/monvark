// Copyright (c) 2013-2015 The btcsuite developers
// Copyright (c) 2015-2023 The Decred developers

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/decred/slog"
	"github.com/edshav/monvark/cl"
	"github.com/jessevdk/go-flags"
	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/dcrutil"
)

const (
	defaultConfigFilename = "monvark.conf"
	defaultLogLevel       = "info"
)

var (
	minerHomeDir         = dcrutil.AppDataDir("monvark", false)
	defaultConfigFile    = filepath.Join(minerHomeDir, defaultConfigFilename)
	defaultAPIPort       = "3333"
	defaultAutocalibrate = 500

	minIntensity = 8
	maxIntensity = 31
	maxWorkSize  = uint32(0xFFFFFFFF - 255)
)

type config struct {
	ListDevices bool `short:"l" long:"listdevices" description:"List number of devices."`
	ShowVersion bool `short:"V" long:"version" description:"Display version information and exit"`

	// Config / log options
	ConfigFile string `short:"C" long:"configfile" description:"Path to configuration file"`
	OpenCLLib  string `long:"opencl-lib" description:"Full path to the OpenCL library to load, for installations the built-in search does not cover"`
	DebugLevel string `short:"d" long:"debuglevel" description:"Logging level {trace, debug, info, warn, error, critical}"`

	// Debugging options
	Profile    string `long:"profile" description:"Enable HTTP profiling on given port -- NOTE port must be between 1024 and 65536"`
	CPUProfile string `long:"cpuprofile" description:"Write CPU profile to the specified file"`

	// Status API options
	APIListen string `long:"apilisten" description:"Interface/port to expose the miner status API on"`

	// Connection details for the node monvark starts.  These are not flags:
	// there is no mode for attaching to a node we did not configure, because a
	// getwork template pays the miningaddr of whichever node served it.
	// nodeStart fills them in with the port and credentials it chose.
	RPCUser     string
	RPCPassword string
	RPCServer   string
	RPCCert     string

	Benchmark bool `short:"B" long:"benchmark" description:"Run in benchmark mode."`

	TestNet bool `long:"testnet" description:"Connect to testnet"`
	SimNet  bool `long:"simnet" description:"Connect to the simulation test network"`

	Autocalibrate     string `short:"A" long:"autocalibrate" description:"Time target in milliseconds to spend executing hashes on the device during each iteration. Single global value or a comma separated list."`
	AutocalibrateInts []int
	Devices           string `short:"D" long:"devices" description:"Single device ID or a comma separated list of device IDs to use."`
	DeviceIDs         []int
	Intensity         string `short:"i" long:"intensity" description:"Intensities (the work size is 2^intensity) per device. Single global value or a comma separated list."`
	IntensityInts     []int
	WorkSize          string `short:"W" long:"worksize" description:"The explicitly declared sizes of the work to do per device (overrides intensity). Single global value or a comma separated list."`
	WorkSizeInts      []uint32

	// Mining options
	MiningAddr string   `long:"miningaddr" description:"Address that block rewards are paid to.  Asked for once on first run and saved to the config file"`
	Mond       string   `long:"mond" description:"Path to the mond binary, for running a development build with no node beside it"`
	AddPeer    []string `long:"addpeer" description:"Bootstrap peer (host:port) for the node monvark starts; repeatable.  Replaces the built-in list."`

	// fileMiningAddr is MiningAddr as it appeared in the config file, captured
	// before the command line is parsed a second time.  After that parse
	// MiningAddr is the effective value and the file's own is unrecoverable,
	// so a disagreement between the two could not otherwise be reported.
	fileMiningAddr string
}

// normalizeAddress returns addr with the passed default port appended if
// there is not already a port specified.
func normalizeAddress(addr string, defaultPort string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return net.JoinHostPort(addr, defaultPort)
	}
	if port == "" {
		// SplitHostPort succeeds on a bare trailing colon -- ":" or
		// "localhost:" -- and reports an empty port, which the net
		// package reads as "pick an ephemeral one" rather than as the
		// default.
		return net.JoinHostPort(host, defaultPort)
	}
	return addr
}

// parseInts parses a per-device setting: either a single value that applies to
// every device, such as "600", or one value per device, such as "450,600".
func parseInts(name, s string) ([]int, error) {
	fields := strings.Split(s, ",")
	values := make([]int, len(fields))
	for i, field := range fields {
		v, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil {
			return nil, fmt.Errorf("could not convert %s %q to int: %w", name,
				field, err)
		}
		values[i] = v
	}
	return values, nil
}

// loadConfig initializes and parses the config using a config file and command
// line options.
//
// The configuration proceeds as follows:
//  1. Start with a default config with sane settings
//  2. Pre-parse the command line to check for an alternative config file
//  3. Load configuration file overwriting defaults with any specified options
//  4. Parse CLI options and overwrite/add any specified options
//
// The above results in btcd functioning properly without any config settings
// while still allowing the user to override settings with config files and
// command line options.  Command line options always take precedence.
func loadConfig() (*config, []string, error) {
	// Default config.
	cfg := config{
		ConfigFile: defaultConfigFile,
		DebugLevel: defaultLogLevel,
	}

	// Create the home directory if it doesn't already exist.
	err := os.MkdirAll(minerHomeDir, 0700)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(-1)
	}

	// Pre-parse the command line options to see if an alternative config
	// file or the version flag was specified.
	preCfg := cfg
	preParser := flags.NewParser(&preCfg, flags.Default)
	_, err = preParser.Parse()
	if err != nil {
		var e *flags.Error
		if !errors.As(err, &e) || e.Type != flags.ErrHelp {
			preParser.WriteHelp(os.Stderr)
		}
		return nil, nil, err
	}

	// Show the version and exit if the version flag was specified.
	appName := filepath.Base(os.Args[0])
	appName = strings.TrimSuffix(appName, filepath.Ext(appName))
	usageMessage := fmt.Sprintf("Use %s -h to show usage", appName)
	if preCfg.ShowVersion {
		fmt.Printf("%s %s version %s (Go version %s %s/%s)\n", appName, gpuLib,
			Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	// Load additional config from file.
	var configFileError error
	parser := flags.NewParser(&cfg, flags.Default)
	err = flags.NewIniParser(parser).ParseFile(preCfg.ConfigFile)
	if err != nil {
		var e *os.PathError
		if !errors.As(err, &e) {
			fmt.Fprintln(os.Stderr, err)
			parser.WriteHelp(os.Stderr)
			return nil, nil, err
		}
		configFileError = err
	}

	// Capture the config file's own miningaddr before the command line
	// overrides it, so a disagreement between the two can be reported.
	cfg.fileMiningAddr = cfg.MiningAddr

	// Parse command line options again to ensure they take precedence.
	remainingArgs, err := parser.Parse()
	if err != nil {
		var e *flags.Error
		if !errors.As(err, &e) || e.Type != flags.ErrHelp {
			parser.WriteHelp(os.Stderr)
		}
		return nil, nil, err
	}

	// Resolve the OpenCL library before anything tries to use it, so a machine
	// with no driver installed gets a message naming the problem instead of a
	// failure inside the first CL call.
	if err := cl.Load(cfg.OpenCLLib); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return nil, nil, err
	}

	if cfg.ListDevices {
		ListDevices()
		os.Exit(0)
	}

	// Multiple networks can't be selected simultaneously.
	numNets := 0
	if cfg.TestNet {
		numNets++
	}
	if cfg.SimNet {
		numNets++
	}
	if numNets > 1 {
		str := "%s: The testnet and simnet params can't be used " +
			"together -- choose one of the two"
		err := fmt.Errorf(str, "loadConfig")
		fmt.Fprintln(os.Stderr, err)
		return nil, nil, err
	}

	// Parse the per-device settings.
	cfg.AutocalibrateInts = []int{defaultAutocalibrate}
	if len(cfg.Autocalibrate) > 0 {
		cfg.AutocalibrateInts, err = parseInts("autocalibration", cfg.Autocalibrate)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return nil, nil, err
		}
	}

	if len(cfg.Devices) > 0 {
		cfg.DeviceIDs, err = parseInts("device", cfg.Devices)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return nil, nil, err
		}
	}

	if len(cfg.Intensity) > 0 {
		cfg.IntensityInts, err = parseInts("intensity", cfg.Intensity)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return nil, nil, err
		}
	}

	for i := range cfg.IntensityInts {
		if (cfg.IntensityInts[i] < minIntensity) ||
			(cfg.IntensityInts[i] > maxIntensity) {
			err := fmt.Errorf("intensity %v not within "+
				"range %v to %v", cfg.IntensityInts[i], minIntensity,
				maxIntensity)
			fmt.Fprintln(os.Stderr, err)
			return nil, nil, err
		}
	}

	if len(cfg.WorkSize) > 0 {
		workSizes, err := parseInts("worksize", cfg.WorkSize)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return nil, nil, err
		}
		cfg.WorkSizeInts = make([]uint32, len(workSizes))
		for i, workSize := range workSizes {
			cfg.WorkSizeInts[i] = uint32(workSize)
		}
	}

	for i := range cfg.WorkSizeInts {
		if cfg.WorkSizeInts[i] < 256 {
			err := fmt.Errorf("too small WorkSize passed: %v, min 256",
				cfg.WorkSizeInts[i])
			fmt.Fprintln(os.Stderr, err)
			return nil, nil, err
		}
		if cfg.WorkSizeInts[i] > maxWorkSize {
			err := fmt.Errorf("too big WorkSize passed: %v, max %v",
				cfg.WorkSizeInts[i], maxWorkSize)
			fmt.Fprintln(os.Stderr, err)
			return nil, nil, err
		}
		if cfg.WorkSizeInts[i]%256 != 0 {
			err := fmt.Errorf("work size %v not a multiple of 256",
				cfg.WorkSizeInts[i])
			fmt.Fprintln(os.Stderr, err)
			return nil, nil, err
		}
	}

	// Set the log level.
	level, ok := slog.LevelFromString(cfg.DebugLevel)
	if !ok {
		err := fmt.Errorf("loadConfig: the specified debug level [%v] is invalid",
			cfg.DebugLevel)
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usageMessage)
		return nil, nil, err
	}
	mainLog.SetLevel(level)
	minrLog.SetLevel(level)

	if cfg.APIListen != "" {
		cfg.APIListen = normalizeAddress(cfg.APIListen, defaultAPIPort)
	}

	switch {
	case cfg.TestNet:
		chainParams = chaincfg.TestNet3Params()
	case cfg.SimNet:
		chainParams = chaincfg.SimNetParams()
	}

	// Warn about missing config file only after all other configuration is
	// done.  This prevents the warning on help messages and invalid
	// options.  Note this should go directly before the return.
	if configFileError != nil {
		mainLog.Warnf("%v", configFileError)
	}

	return &cfg, remainingArgs, nil
}
