// SPDX-License-Identifier: MIT

package multiline

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// spdxHeader opens every Go file in the module, so a legal review handed any
// single file can tell its license without tracing it back to LICENSE.
const spdxHeader = "// SPDX-License-Identifier: MIT"

// TestSPDXHeaders asserts that every Go file in the module, tests and examples
// included, starts with spdxHeader followed by a blank line. The blank line is
// load-bearing: without it the header joins the package doc comment, whose
// first sentence pkg.go.dev shows as the package synopsis.
func TestSPDXHeaders(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.SplitN(string(src), "\n", 3)
		assert.True(t, len(lines) == 3 && lines[0] == spdxHeader && lines[1] == "",
			"%s must start with %q and a blank line", path, spdxHeader)
		return nil
	})
	assert.NoError(t, err)
}
