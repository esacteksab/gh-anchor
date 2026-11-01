// SPDX-License-Identifier: MIT

// Package datasource looks up available versions from package registries and GitHub.
package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/esacteksab/httpcache"
	"github.com/esacteksab/httpcache/diskcache"
	"github.com/google/go-github/v91/github"

	"github.com/esacteksab/gh-anchor/registry"
)

// AppName is used for the cache directory and User-Agent.
const AppName = "gh-anchor"

const (
	maxBodyBytes = 64 << 20
	httpTimeout  = 60 * time.Second
	cacheDirPerm = 0o750
)

// ErrNotFound is wrapped by errors for packages, versions or repositories that don't exist.
var ErrNotFound = errors.New("not found")

// IsNotFound reports whether err means the thing looked up doesn't exist (an HTTP 404
// from npm, Node.js, GitHub or a container registry).
func IsNotFound(err error) bool {
	if errors.Is(err, ErrNotFound) || registry.IsNotFound(err) {
		return true
	}
	var er *github.ErrorResponse
	return errors.As(err, &er) && er.Response != nil &&
		er.Response.StatusCode == http.StatusNotFound
}

// CacheDir returns the on-disk HTTP cache directory for gh-anchor.
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user cache directory: %w", err)
	}
	return filepath.Join(base, AppName), nil
}

// NewCachedHTTPClient returns an HTTP client backed by a disk cache in CacheDir.
// Responses are reused according to their Cache-Control/ETag headers, which keeps
// repeated runs fast and well inside API rate limits.
func NewCachedHTTPClient() (*http.Client, error) {
	dir, err := CacheDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, cacheDirPerm); err != nil {
		return nil, fmt.Errorf("could not create cache directory '%s': %w", dir, err)
	}
	return &http.Client{
		Transport: httpcache.NewTransport(diskcache.New(dir)),
		Timeout:   httpTimeout,
	}, nil
}

// getJSON GETs url and decodes the JSON response into v.
func getJSON(ctx context.Context, client *http.Client, url, accept, userAgent string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("GET %s: %s: %w", url, resp.Status, ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(v); err != nil {
		return fmt.Errorf("decoding %s: %w", url, err)
	}
	return nil
}
