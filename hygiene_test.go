package hygiene

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSourceHasNoHiddenCharacters fails if any text file contains control,
// bidirectional or invisible characters, which can make code read
// differently from how it compiles. Tests build such strings with escapes
// such as "\u200b" instead.
func TestSourceHasNoHiddenCharacters(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != "." && d.Name() != ".github" {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".md", ".yml", ".yaml", ".mod":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		line := 1
		for i, r := range string(data) {
			if r == '\n' {
				line++
				continue
			}
			if r == utf8.RuneError || hidden(r) {
				t.Errorf("%s:%d: hidden character U+%04X at byte %d", path, line, r, i)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func hidden(r rune) bool {
	switch {
	case r == '\t':
		return false
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2060 && r <= 0x2069, r == 0xfeff:
		return true
	case r >= 0xe0000 && r <= 0xe007f:
		return true
	}
	return false
}
