// Package tlsinfo reports the TLS key-exchange compatibility settings that are
// in effect, so a deployment's ClientHello can be identified from its logs.
//
// Go reads GODEBUG once at init, from the environment; the process cannot
// change it afterwards. Which groups a default ClientHello offers therefore
// depends on how the process was started, not on the code. That difference is
// invisible in every other signal, and one book's edge treats the two shapes
// differently, so the value is logged at startup.
package tlsinfo

import (
	"os"
	"strings"
)

// CompatGODEBUG reports the GODEBUG TLS compatibility settings that are
// switched off, as they would be written in GODEBUG, or "default" when none
// are. Only the two settings that decide the default key exchanges are read:
// tlsmlkem (all ML-KEM hybrids, Go 1.24) and tlssecpmlkem (the P-curve ones,
// Go 1.26). A later field for the same key wins, as in Go's own parsing.
func CompatGODEBUG() string {
	const (
		allMLKEM  = "tlsmlkem"     // X25519MLKEM768 and the P-curve hybrids
		secpMLKEM = "tlssecpmlkem" // SecP256r1MLKEM768, SecP384r1MLKEM1024
	)
	value := map[string]string{}
	var order []string
	for _, field := range strings.Split(os.Getenv("GODEBUG"), ",") {
		key, v, ok := strings.Cut(field, "=")
		if !ok || (key != allMLKEM && key != secpMLKEM) {
			continue
		}
		if _, seen := value[key]; !seen {
			order = append(order, key)
		}
		value[key] = v
	}
	var off []string
	for _, key := range order {
		if value[key] == "0" {
			off = append(off, key+"=0")
		}
	}
	if len(off) == 0 {
		return "default"
	}
	return strings.Join(off, ",")
}
