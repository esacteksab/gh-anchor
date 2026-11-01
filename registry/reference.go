// SPDX-License-Identifier: MIT

// Package registry is a small client for the OCI Distribution API: it lists a
// repository's tags and resolves a tag to its manifest digest. It speaks the
// token-auth flow used by Docker Hub, GHCR and most other registries.
package registry

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	// DockerHub is the canonical registry name for Docker Hub.
	DockerHub = "docker.io"
	// dockerHubAPI is the host that actually serves the Docker Hub registry API.
	dockerHubAPI = "registry-1.docker.io"
)

var (
	repoComponentRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
	tagRe           = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	digestRe        = regexp.MustCompile(`^[a-z0-9]+(?:[.+_-][a-z0-9]+)*:[a-zA-Z0-9=_-]{32,}$`)
)

// Reference is a parsed image reference such as `ghcr.io/owner/tool:1.2.3@sha256:...`.
type Reference struct {
	Name     string // The repository exactly as written, e.g. "ubuntu" or "ghcr.io/owner/tool"
	Registry string // e.g. "docker.io", "ghcr.io", "localhost:5000"
	Repo     string // Path within the registry, e.g. "library/ubuntu"
	Tag      string
	Digest   string
}

// ParseReference parses an image reference using Docker's normalization rules:
// a first path component containing "." or ":" (or equal to "localhost") is a
// registry host; otherwise the image lives on Docker Hub, under "library/" for
// single-component names.
func ParseReference(s string) (Reference, error) {
	var ref Reference
	rest := s
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		ref.Digest = rest[at+1:]
		rest = rest[:at]
		if !digestRe.MatchString(ref.Digest) {
			return Reference{}, fmt.Errorf("invalid digest in image reference %q", s)
		}
	}
	if colon := strings.LastIndexByte(rest, ':'); colon > strings.LastIndexByte(rest, '/') {
		ref.Tag = rest[colon+1:]
		rest = rest[:colon]
		if !tagRe.MatchString(ref.Tag) {
			return Reference{}, fmt.Errorf("invalid tag in image reference %q", s)
		}
	}
	if rest == "" {
		return Reference{}, fmt.Errorf("invalid image reference %q", s)
	}
	ref.Name = rest

	path := rest
	if first, remainder, ok := strings.Cut(rest, "/"); ok &&
		(strings.ContainsAny(first, ".:") || first == "localhost") {
		ref.Registry = strings.ToLower(first)
		path = remainder
	} else {
		ref.Registry = DockerHub
	}
	if ref.Registry == "index.docker.io" || ref.Registry == dockerHubAPI {
		ref.Registry = DockerHub
	}
	if ref.Registry == DockerHub && !strings.Contains(path, "/") {
		path = "library/" + path
	}
	for c := range strings.SplitSeq(path, "/") {
		if !repoComponentRe.MatchString(c) {
			return Reference{}, fmt.Errorf("invalid repository name in image reference %q", s)
		}
	}
	ref.Repo = path
	return ref, nil
}

// Repository returns the fully-qualified repository, e.g. "docker.io/library/ubuntu".
func (r Reference) Repository() string { return r.Registry + "/" + r.Repo }

// String renders the reference using the name as originally written.
func (r Reference) String() string {
	s := r.Name
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}
	return s
}

// apiHost returns the host serving the registry API for r.
func (r Reference) apiHost() string {
	if r.Registry == DockerHub {
		return dockerHubAPI
	}
	return r.Registry
}

// scheme returns "http" for loopback registries (as Docker does) and "https" otherwise.
func (r Reference) scheme() string {
	host := r.Registry
	if h, _, ok := strings.Cut(host, ":"); ok && !strings.HasPrefix(host, "[") {
		host = h
	}
	switch {
	case host == "localhost", host == "127.0.0.1", strings.HasPrefix(host, "[::1]"):
		return "http"
	default:
		return "https"
	}
}
