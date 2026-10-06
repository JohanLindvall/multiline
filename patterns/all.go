// SPDX-License-Identifier: MIT

package patterns

// All lists the bundled state sets; the default matcher of multiline.New is
// MustCompile(All...). Compile a subset to aggregate only some formats, or
// append your own sets to extend it. Like the individual set variables, All
// is exported data: treat it as read-only and build new slices instead of
// mutating it.
//
// The order matters where two formats share a line shape, because
// multiline.Entry.Match reports the format of the last accepting line and ties
// are broken by this order: [DotNet] comes before [Java] so that a .NET trace
// whose frames also match Java's is still reported as "dotnet".
var All = []StateSet{Go, DotNet, Java, NodeJS, Python, Ruby, Rust, PHP, Elixir}
