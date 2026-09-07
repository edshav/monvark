#!/usr/bin/env bash
#
# Copyright (c) 2020-2026 The Decred developers
# Use of this source code is governed by an ISC
# license that can be found in the LICENSE file.
#
# Usage:
#   ./run_tests.sh

set -e

go version

# Run tests.  The GPU tests skip when no OpenCL device is present, so this is
# also the CI entrypoint.
go test -v ./...

# Run linters.
golangci-lint run

echo "-----------------------------"
echo "Tests completed successfully!"
