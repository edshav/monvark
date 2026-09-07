// Command monvark is a BLAKE3 GPU miner for Monetarium.
package main

import (
	"context"
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

	m, err := NewMiner(ctx)
	if err != nil {
		mainLog.Criticalf("Error initializing miner: %v", err)
		return err
	}
	if cfg.APIListen != "" {
		go RunMonitor(m)
	}
	m.Run(ctx)

	return nil
}

func main() {
	// Work around defer not working after os.Exit()
	if err := monvarkMain(); err != nil {
		os.Exit(1)
	}
}
