package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTLSCompatSettingIsDeployed guards a deployment fact rather than a code
// one: the container drops the P-curve ML-KEM key exchanges from its default
// TLS ClientHello, which is what makes one book's REST API answer with JSON
// instead of its branded HTML 403 block page. The setting lives in the image
// default and again in the Fly env; if either silently loses it, the book goes
// down with an HTTP 403 and nothing in the failure points at TLS. That is
// exactly the hour-long detour this test is here to prevent, so both places
// are asserted.
func TestTLSCompatSettingIsDeployed(t *testing.T) {
	const setting = "tlssecpmlkem=0"
	root := filepath.Join("..", "..")

	t.Run("image", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
		if err != nil {
			t.Fatal(err)
		}
		lastFrom, envAt := -1, -1
		for i, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			switch strings.ToUpper(fields[0]) {
			case "FROM":
				lastFrom = i
			case "ENV":
				// "ENV GODEBUG=..." or the older "ENV GODEBUG ..." form.
				if strings.Contains(line, "GODEBUG") && strings.Contains(line, setting) {
					envAt = i
				}
			}
		}
		if lastFrom < 0 {
			t.Fatalf("no FROM in Dockerfile")
		}
		if envAt < lastFrom {
			t.Errorf("Dockerfile's runtime stage does not set GODEBUG=%s after line %d; the binary reads GODEBUG at init, so it cannot be set from main", setting, lastFrom+1)
		}
	})

	t.Run("fly env", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(root, "fly.toml"))
		if err != nil {
			t.Fatal(err)
		}
		re := regexp.MustCompile(`(?m)^\s*GODEBUG\s*=\s*"[^"]*` + regexp.QuoteMeta(setting) + `[^"]*"\s*$`)
		if !re.Match(raw) {
			t.Errorf("fly.toml does not set GODEBUG = \"...%s...\"; the deployed ClientHello would change silently", setting)
		}
	})
}
