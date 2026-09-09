// Package version records what this server build calls itself.
package version

// Current is the server's semantic version, shown to users through the
// changelog and reported by ListChangelog so a client can tell which server
// releases are new to it.
//
// Bumped by hand, once per feature, the same way WellSpent-web's package.json
// is — see the workspace CLAUDE.md's version-bump rules. It is deliberately a
// human-readable semver rather than a git SHA injected at build time: a reader
// opening "what's new" is being shown release notes, and notes have to hang
// off something they can recognise.
const Current = "1.3.0"
