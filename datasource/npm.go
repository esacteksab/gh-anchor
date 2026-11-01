// SPDX-License-Identifier: MIT

package datasource

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// DefaultNPMRegistry is the public npm registry.
const DefaultNPMRegistry = "https://registry.npmjs.org"

// NPM queries an npm registry.
type NPM struct {
	BaseURL   string
	HTTP      *http.Client
	UserAgent string
}

// Package is the subset of npm package metadata gh-anchor needs.
type Package struct {
	Versions []string          // Published, non-deprecated versions
	DistTags map[string]string // e.g. latest -> 12.16.1
	// Hashes maps a version to the sha512 of its tarball in corepack's format,
	// "sha512.<hex>" (npm publishes it base64-encoded as dist.integrity).
	Hashes map[string]string
}

// HashFromIntegrity converts npm's "sha512-<base64>" integrity to "sha512.<hex>".
func HashFromIntegrity(integrity string) string {
	algo, b64, ok := strings.Cut(integrity, "-")
	if !ok || algo != "sha512" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return ""
	}
	return "sha512." + hex.EncodeToString(raw)
}

// Package fetches the abbreviated metadata for name (e.g. "pnpm" or "@scope/pkg").
func (n *NPM) Package(ctx context.Context, name string) (Package, error) {
	base := strings.TrimRight(n.BaseURL, "/")
	if base == "" {
		base = DefaultNPMRegistry
	}
	// Scoped packages keep the "@" but escape the "/": @scope%2Fname.
	u := base + "/" + strings.Replace(url.PathEscape(name), "%40", "@", 1)

	var doc struct {
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			Deprecated any `json:"deprecated"`
			Dist       struct {
				Integrity string `json:"integrity"`
			} `json:"dist"`
		} `json:"versions"`
	}
	// The abbreviated "corgi" document is much smaller than the full packument.
	accept := "application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8"
	if err := getJSON(ctx, n.HTTP, u, accept, n.UserAgent, &doc); err != nil {
		return Package{}, err
	}
	pkg := Package{DistTags: doc.DistTags, Hashes: map[string]string{}}
	for v, meta := range doc.Versions {
		if h := HashFromIntegrity(meta.Dist.Integrity); h != "" {
			pkg.Hashes[v] = h
		}
		if dep, ok := meta.Deprecated.(string); ok && dep != "" {
			continue
		}
		pkg.Versions = append(pkg.Versions, v)
	}
	sort.Strings(pkg.Versions)
	return pkg, nil
}
