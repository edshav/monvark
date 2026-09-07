package main

import (
	"os"

	"github.com/decred/slog"
)

// Logging goes to stdout only.  Retention is the platform's job: shell
// redirection, journald, or a Windows service's own output redirection.
var (
	// backendLog is the logging backend used to create all loggers.
	backendLog = slog.NewBackend(os.Stdout)

	mainLog = backendLog.Logger("MAIN")
	minrLog = backendLog.Logger("MINR")
)
