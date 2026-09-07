// Command monvark is a BLAKE3 GPU miner for Monetarium.
package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"syscall"
)

var (
	cfg *config
)

func monvarkMain() error {
	// Load configuration and parse command line.  This function also
	// initializes logging and configures it accordingly.
	tcfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	cfg = tcfg

	// Show version at startup.
	mainLog.Infof("Version %s %s (Go version %s %s/%s)", Version, gpuLib,
		runtime.Version(), runtime.GOOS, runtime.GOARCH)

	// Enable http profiling server if requested.
	if cfg.Profile != "" {
		go func() {
			listenAddr := net.JoinHostPort("", cfg.Profile)
			mainLog.Infof("Creating profiling server "+
				"listening on %s", listenAddr)
			profileRedirect := http.RedirectHandler("/debug/pprof",
				http.StatusSeeOther)
			http.Handle("/", profileRedirect)
			err := http.ListenAndServe(listenAddr, nil)
			if err != nil {
				mainLog.Errorf("Unable to create profiler: %v", err)
				os.Exit(1)
			}
		}()
	}

	// Write cpu profile if requested.
	if cfg.CPUProfile != "" {
		f, err := os.Create(cfg.CPUProfile)
		if err != nil {
			mainLog.Errorf("Unable to create cpu profile: %v", err)
			return err
		}
		pprof.StartCPUProfile(f)
		defer f.Close()
		defer pprof.StopCPUProfile()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt,
		syscall.SIGTERM)

	// Restore the default signal handling as soon as the first signal has been
	// delivered, so a second one kills a shutdown that is not progressing.
	go func() {
		<-ctx.Done()
		minrLog.Info("Received shutdown signal.  Shutting down...")
		stop()
	}()

	// Build the devices first.  Everything after this point can take minutes,
	// and a user whose driver is broken should not wait through it.  This is
	// the full build rather than a cheap enumeration on purpose: getCLDevices
	// only enumerates, while NewDevice creates the context and compiles
	// blake3.cl, which is where a driver that enumerates perfectly still
	// fails.  A device consumes no GPU time until SetWork feeds it.
	workDone := make(chan []byte, 10)
	devices, err := newMinerDevs(workDone)
	if err != nil {
		mainLog.Criticalf("Error initializing devices: %v", err)
		return err
	}
	if len(devices) == 0 {
		mainLog.Critical("No devices started")
		return errors.New("no devices started")
	}

	// newMinerDevs takes no context, so an interrupt during the kernel compile
	// is only noticed once it returns.  Stop here rather than going on to
	// prompt for an address the user no longer wants to give.
	if ctx.Err() != nil {
		return nil
	}

	// Benchmark mode needs no node, no address and no chain: it is the only
	// path that exercises the mining loop on its own.
	var payoutAddr string
	if !cfg.Benchmark {
		payoutAddr, err = resolvePayout(cfg)
		if err != nil {
			mainLog.Criticalf("%v", err)
			return err
		}

		node, err := nodeStart(ctx, cfg, payoutAddr)
		if err != nil {
			// A cancelled context here means the user interrupted the start,
			// not that the node is broken.  Exiting non-zero would make a
			// deliberate stop look like a fault to a service manager.
			if ctx.Err() != nil {
				return nil
			}
			mainLog.Criticalf("Unable to start the node: %v", err)
			return err
		}
		defer node.Stop()

		if err := node.WaitSynced(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			mainLog.Criticalf("%v", err)
			return err
		}
	}

	m, err := NewMiner(ctx, devices, workDone, payoutAddr)
	if err != nil {
		mainLog.Criticalf("Error initializing miner: %v", err)
		return err
	}
	if cfg.APIListen != "" {
		go RunMonitor(m)
	}
	return m.Run(ctx)
}

func main() {
	// Work around defer not working after os.Exit()
	if err := monvarkMain(); err != nil {
		os.Exit(1)
	}
}
