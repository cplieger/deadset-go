package main

import (
	"runtime/debug"
	"strings"
)

// develVersion is the version of a build the toolchain stamped no module version
// on: a test binary, or a build of a tree that is not under version control. It is
// shaped like every other version this command writes, because a report names the
// analyzer's version and the report schema accepts a semantic version and nothing
// else.
const develVersion = "0.0.0-devel"

// version is this analyzer's own version, which the build carries rather than the
// source. It moves independently of the Contract version the analyzer implements,
// which is [config.ContractVersion].
//
// The value is the module version the build stamped with the leading v removed: the
// tag under go install, and the pseudo-version the toolchain derives from the
// checkout under go build, carrying +dirty where the tree holds uncommitted
// changes. A build the toolchain records no module version for is develVersion.
//
// It is one function because a version written in more than one place is two facts
// that can disagree: the version verb, describe, the report envelope and a baseline
// this command writes all read this one.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return develVersion
	}
	return versionOf(info.Main.Version)
}

// versionOf is the version one stamped module version names. A module version is
// the letter v followed by a semantic version, so a value without that prefix names
// none: the toolchain writes (devel) for a main module it has no version for, and
// the empty string where a build carries no module version at all.
func versionOf(stamped string) string {
	semver, isVersion := strings.CutPrefix(stamped, "v")
	if !isVersion || semver == "" {
		return develVersion
	}
	return semver
}
