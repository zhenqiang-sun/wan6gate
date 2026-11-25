// Package version is the single source of truth for the wan6gate build identity.
//
// All three vars are overridden by `scripts/build.sh` via -ldflags at link time.
// Defaults below are used by `go run` / `go test` / IDE builds where no ldflags
// are set, so the binary still self-identifies.
//
// Number is derived from `git describe --tags --always --dirty` in build.sh:
//   - On a release tag (e.g. `git tag v0.2.2`)  → Number = "0.2.2"
//   - 3 commits past v0.2.2                     → Number = "0.2.2-3-gabc1234"
//   - Working tree has uncommitted edits        → suffix " -dirty" appended
//
// To cut a new release: `git commit ...; git tag v0.2.2; ./scripts/build.sh`.
// No code edit needed.
package version

var (
	// Number is the semver-ish display string. See package doc.
	Number = "0.3.0"
	// Commit is the short SHA of the build's HEAD (≈10 chars), or "dev".
	Commit = "dev"
	// BuildTime is the UTC RFC-3339 timestamp the binary was linked, or "unknown".
	BuildTime = "unknown"
)

// Full returns "wan6gate <number>" or "wan6gate <number> (<commit>)" when commit is known.
// When `Number` already encodes the commit (via `git describe`'s `-N-gXXXX` suffix),
// the parenthesised commit is redundant and omitted.
func Full() string {
	if Commit == "" || Commit == "dev" {
		return "wan6gate " + Number
	}
	// Heuristic: if Number already contains "-g<sha>" from git describe, don't repeat.
	for i := 0; i < len(Number)-2; i++ {
		if Number[i] == '-' && Number[i+1] == 'g' {
			return "wan6gate " + Number
		}
	}
	return "wan6gate " + Number + " (" + Commit + ")"
}
