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
	minerHomeDir          = dcrutil.AppDataDir("monvark", false)
	nodeHomeDir           = dcrutil.AppDataDir("monetarium", false)
	defaultConfigFile     = filepath.Join(minerHomeDir, defaultConfigFilename)
	defaultRPCServer      = "localhost"
	defaultRPCCertFile    = filepath.Join(nodeHomeDir, "rpc.cert")
	defaultRPCPortMainNet = "9509"
	defaultRPCPortTestNet = "19509"
	defaultRPCPortSimNet  = "19956"
	defaultAPIPort        = "3333"
	defaultAutocalibrate  = 500

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

	// RPC connection options
	RPCUser     string `short:"u" long:"rpcuser" description:"RPC username"`
	RPCPassword string `short:"P" long:"rpcpass" default-mask:"-" description:"RPC password"`
	RPCServer   string `short:"s" long:"rpcserver" description:"RPC server to connect to"`
	RPCCert     string `short:"c" long:"rpccert" description:"RPC server certificate chain for validation"`
	Proxy       string `long:"proxy" description:"Connect via SOCKS5 proxy (eg. 127.0.0.1:9050)"`
	ProxyUser   string `long:"proxyuser" description:"Username for proxy server"`
	ProxyPass   string `long:"proxypass" default-mask:"-" description:"Password for proxy server"`

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
}

// normalizeAddress returns addr with the passed default port appended if
// there is not already a port specified.
func normalizeAddress(addr string, defaultPort string) string {
	_, _, err := net.SplitHostPort(addr)
	if err != nil {
		return net.JoinHostPort(addr, defaultPort)
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

// cleanAndExpandPath expands environement variables and leading ~ in the
// passed path, cleans the result, and returns it.
func cleanAndExpandPath(path string) string {
	// Expand initial ~ to OS specific home directory.
	if strings.HasPrefix(path, "~") {
		homeDir := filepath.Dir(minerHomeDir)
		path = strings.Replace(path, "~", homeDir, 1)
	}

	// NOTE: The os.ExpandEnv doesn't work with Windows-style %VARIABLE%,
	// but they variables can still be expanded via POSIX-style $VARIABLE.
	return filepath.Clean(os.ExpandEnv(path))
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
		RPCServer:  defaultRPCServer,
		RPCCert:    defaultRPCCertFile,
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

	// Handle environment variable expansion in the RPC certificate path.
	cfg.RPCCert = cleanAndExpandPath(cfg.RPCCert)

	var defaultRPCPort string
	switch {
	case cfg.TestNet:
		defaultRPCPort = defaultRPCPortTestNet
		chainParams = chaincfg.TestNet3Params()
	case cfg.SimNet:
		defaultRPCPort = defaultRPCPortSimNet
		chainParams = chaincfg.SimNetParams()
	default:
		defaultRPCPort = defaultRPCPortMainNet
	}

	// Add default port to RPC server based on --testnet flag
	// if needed.
	cfg.RPCServer = normalizeAddress(cfg.RPCServer, defaultRPCPort)

	// Warn about missing config file only after all other configuration is
	// done.  This prevents the warning on help messages and invalid
	// options.  Note this should go directly before the return.
	if configFileError != nil {
		mainLog.Warnf("%v", configFileError)
	}

	return &cfg, remainingArgs, nil
}
