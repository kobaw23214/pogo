// Package officeruntime serves the reviewed ONLYOFFICE bridge assets.
package officeruntime

import (
	"embed"
	"errors"
)

//go:embed bridge
var bridgeFS embed.FS

// Asset never accepts a caller-selected filesystem path.
func Asset(name string) ([]byte, error) {
	switch name {
	case "config.json", "index.html", "plugin.js", "sdk.js":
		return bridgeFS.ReadFile("bridge/" + name)
	default:
		return nil, errors.New("unknown Office bridge asset")
	}
}
