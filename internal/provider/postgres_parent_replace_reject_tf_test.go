package provider

import (
	"strings"
)

// The wire-fake echoes back the requested name: setPostgresStateResource overwrites
// state.name from the response, so a fixed name would drift from config.
func pgNameFromPath(p string) string {
	i := strings.LastIndex(p, "/postgres/")
	if i < 0 {
		return ""
	}
	rest := p[i+len("/postgres/"):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	return rest
}
