package patterns

// All lists the bundled state sets; the default matcher of multiline.New is
// MustCompile(All...). Compile a subset to aggregate only some formats, or
// append your own sets to extend it.
var All = []StateSet{Go, Java, Python, DotNet, Ruby, Rust, PHP}
