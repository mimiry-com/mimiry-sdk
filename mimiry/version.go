package mimiry

import "runtime/debug"

// modulePath is this module's path, as go.mod declares it.
const modulePath = "github.com/mimiry-com/mimiry-sdk"

// devVersion is what a build reports when nothing recorded a version.
const devVersion = "0.0.0-dev"

// linkedVersion is set by release builds:
//
//	go build -ldflags "-X github.com/mimiry-com/mimiry-sdk/mimiry.linkedVersion=v0.1.0" ./cmd/mimiry
var linkedVersion string

// Version is this library's version, sent in the User-Agent and shown by
// `mimiry --version`.
//
// It is the version Go recorded when the binary was built, so it needs no
// constant to keep in step: `go install …@v0.1.0` records v0.1.0, `@latest`
// before the first tag records a pseudo-version naming the commit, and a
// `go build` in a checkout records the checkout's commit, marked +dirty when
// it has uncommitted changes. A release build sets linkedVersion instead.
var Version = buildVersion(linkedVersion, debug.ReadBuildInfo)

func buildVersion(linked string, read func() (*debug.BuildInfo, bool)) string {
	if linked != "" {
		return linked
	}
	info, ok := read()
	if !ok {
		return devVersion
	}
	v := ""
	if info.Main.Path == modulePath {
		// The CLI, or a test binary of this module.
		v = info.Main.Version
	} else {
		// A program that imports the library.
		for _, d := range info.Deps {
			if d.Path != modulePath {
				continue
			}
			v = d.Version
			if d.Replace != nil {
				// A replace directive: the code is the replacement's.
				v = d.Replace.Version
			}
			break
		}
	}
	if v == "" || v == "(devel)" {
		return devVersion
	}
	return v
}
