// SPDX-License-Identifier: MIT

package datasource

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// DefaultNodeDist is the Node.js distribution site.
const DefaultNodeDist = "https://nodejs.org/dist"

// Node queries the Node.js release index (index.json).
type Node struct {
	BaseURL   string
	HTTP      *http.Client
	UserAgent string
}

// NodeRelease is a Node.js release.
type NodeRelease struct {
	Version string // e.g. "v22.23.4"
	LTS     bool
}

// Releases returns every Node.js release listed in index.json.
func (n *Node) Releases(ctx context.Context) ([]NodeRelease, error) {
	base := strings.TrimRight(n.BaseURL, "/")
	if base == "" {
		base = DefaultNodeDist
	}
	var index []struct {
		Version string          `json:"version"`
		LTS     json.RawMessage `json:"lts"` // false or the LTS codename
	}
	if err := getJSON(ctx, n.HTTP, base+"/index.json", "application/json", n.UserAgent, &index); err != nil {
		return nil, err
	}
	out := make([]NodeRelease, 0, len(index))
	for _, r := range index {
		lts := len(r.LTS) > 0 && string(r.LTS) != "false" && string(r.LTS) != "null"
		out = append(out, NodeRelease{Version: r.Version, LTS: lts})
	}
	return out, nil
}
