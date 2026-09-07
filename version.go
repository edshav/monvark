/*
 * Copyright (c) 2013, 2014 The btcsuite developers
 * Copyright (c) 2015-2023 The Decred developers
 * Copyright (c) 2016 Dario Nieuwenhuis
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package main

import (
	"runtime/debug"
	"strings"
)

// Version is the application version per the semantic versioning 2.0.0 spec
// (https://semver.org/).
//
// It is defined as a variable so it can be overridden during the build process
// with '-ldflags "-X main.Version=fullsemver"' if needed.
//
// The expected process for setting the version in releases is to create a
// release branch of the form 'release-vMAJOR.MINOR' and drop the pre-release
// portion on it, while this branch keeps a pre-release of 'pre'.  That way a
// build from source is distinct from a reproducible release build.
var Version = "2.1.0-pre"

// vcsCommitID attempts to return the version control system short commit hash
// that was used to build the binary.  It currently only detects git commits.
func vcsCommitID() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var vcs, revision string
	for _, bs := range bi.Settings {
		switch bs.Key {
		case "vcs":
			vcs = bs.Value
		case "vcs.revision":
			revision = bs.Value
		}
	}
	if vcs == "" {
		return ""
	}
	if vcs == "git" && len(revision) > 9 {
		revision = revision[:9]
	}
	return revision
}

func init() {
	// Append the commit as build metadata unless the version already carries
	// some, which is the case for a version set via linker flags.
	if commit := vcsCommitID(); commit != "" && !strings.Contains(Version, "+") {
		Version += "+" + commit
	}
}
