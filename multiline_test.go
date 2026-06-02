package multiline

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_Unit_Multiline(t *testing.T) {
	err := filepath.WalkDir("tests", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() {
			t.Run(path, func(t *testing.T) {
				file, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				firstSplit := bytes.SplitN(file, []byte("\n"), 2)
				if len(firstSplit) < 2 {
					t.Fatal("Invalid test file, must have at least two lines")
				}
				file = firstSplit[1]
				var expected []int
				for _, part := range bytes.Split(firstSplit[0], []byte{','}) {
					var val int
					if val, err = strconv.Atoi(strings.TrimSpace(string(part))); err != nil {
						t.Fatal(err)
					}
					expected = append(expected, val)
				}

				var expectedStr []string
				split := bytes.Split(file, []byte("\n"))
				for _, lines := range expected {
					tmp := lines
					if tmp > len(split) {
						tmp = len(split)
					}
					expectedStr = append(expectedStr, string(bytes.Join(split[:tmp], []byte("\n"))))
					split = split[tmp:]
				}

				var actualLineCounts []int
				var actualLines []string
				ml := New(func(_ context.Context, line, exit, _ string) error {
					actualLines = append(actualLines, line)
					actualLineCounts = append(actualLineCounts, bytes.Count([]byte(line), []byte{'\n'})+1)
					return nil
				})
				for _, line := range bytes.Split(file, []byte("\n")) {
					err := ml.Add(context.Background(), string(line), "key", "")
					assert.NoError(t, err)
				}
				err = ml.Stop(context.Background())
				assert.NoError(t, err)
				var msg strings.Builder
				for _, line := range actualLines {
					fmt.Fprintf(&msg, "==========================================================\n%s\n==========================================================\n", line)
				}
				assert.Equal(t, expected, actualLineCounts, msg.String())
				assert.Equal(t, expectedStr, actualLines, msg.String())
			})
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}
}
