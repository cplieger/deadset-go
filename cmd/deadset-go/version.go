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

// version is the module version the build stamped, with the leading v removed: the
// tag under go install, or the pseudo-version go build derives from the checkout,
// carrying +dirty for uncommitted changes. A build with no module version is
// develVersion. Every document naming the analyzer's version reads this one.
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
