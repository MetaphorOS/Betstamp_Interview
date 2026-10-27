// Package archtest enforces that only book packages and commands know which
// books exist, so a third book never requires touching the framework.
package archtest

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoBookNamesOutsideBookPackages(t *testing.T) {
	root := filepath.Join("..", "..")
	self, _ := filepath.Abs(".")
	banned := []string{"draft" + "kings", "fan" + "duel", "bet" + "mgm"}
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			abs, _ := filepath.Abs(path)
			if rel == "internal/books" || rel == "cmd" || rel == "testdata" || (rel != "." && strings.HasPrefix(d.Name(), ".")) || abs == self {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		low := strings.ToLower(string(raw))
		for _, b := range banned {
			if strings.Contains(low, b) {
				t.Errorf("%s mentions %q; book-specific code belongs in internal/books or cmd", rel, b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 10 {
		t.Fatalf("only %d files checked; walk is wrong", checked)
	}
	t.Logf("checked %d files", checked)
}
