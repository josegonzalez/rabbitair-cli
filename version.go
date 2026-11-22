package main

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
)

// version is set at build time by the release workflow with
// -ldflags "-X main.version=v1.2.3".
var version = ""

// buildVersion returns the release version, or the module version for
// binaries built with `go install`, or "dev".
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func cmdVersion(out io.Writer) error {
	_, err := fmt.Fprintf(out, "rabbitair-cli %s (%s/%s, %s)\n", buildVersion(), runtime.GOOS, runtime.GOARCH, runtime.Version())
	return err
}
