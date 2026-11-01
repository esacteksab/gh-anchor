// SPDX-License-Identifier: MIT

package registry_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/esacteksab/gh-anchor/internal/testserver"
	"github.com/esacteksab/gh-anchor/registry"
)

func TestParseReference(t *testing.T) {
	tests := []struct {
		in                          string
		registry, repo, tag, digest string
		repository                  string
		wantErr                     bool
	}{
		{
			in:         "ubuntu",
			registry:   "docker.io",
			repo:       "library/ubuntu",
			repository: "docker.io/library/ubuntu",
		},
		{
			in:       "ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852",
			registry: "docker.io", repo: "library/ubuntu", tag: "24.04",
			digest:     "sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852",
			repository: "docker.io/library/ubuntu",
		},
		{
			in: "esacteksab/go:1.27.3", registry: "docker.io", repo: "esacteksab/go", tag: "1.27.3",
			repository: "docker.io/esacteksab/go",
		},
		{
			in: "ghcr.io/astral-sh/uv:0.13.6", registry: "ghcr.io", repo: "astral-sh/uv", tag: "0.13.6",
			repository: "ghcr.io/astral-sh/uv",
		},
		{
			in: "localhost:5000/team/app:v1", registry: "localhost:5000", repo: "team/app", tag: "v1",
			repository: "localhost:5000/team/app",
		},
		{
			in: "index.docker.io/library/golang:1.26", registry: "docker.io", repo: "library/golang", tag: "1.26",
			repository: "docker.io/library/golang",
		},
		{in: "Ubuntu:24.04", wantErr: true},
		{in: "ubuntu:bad tag", wantErr: true},
		{in: "ubuntu@sha256:short", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			ref, err := registry.ParseReference(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.registry, ref.Registry)
			assert.Equal(t, tt.repo, ref.Repo)
			assert.Equal(t, tt.tag, ref.Tag)
			assert.Equal(t, tt.digest, ref.Digest)
			assert.Equal(t, tt.repository, ref.Repository())
			assert.Equal(t, tt.in, ref.String())
		})
	}
}

func newClient(creds *registry.Credentials) *registry.Client {
	c := registry.NewClient(creds, "gh-anchor-test")
	c.Sleep = func(context.Context, time.Duration) {}
	return c
}

func TestTagsAndDigestWithAuthAndPagination(t *testing.T) {
	reg := testserver.NewRegistry(map[string][]string{
		"library/golang": {"1.26.10", "1.26.11", "1.27.3", "1.27.3-alpine", "latest"},
	})
	defer reg.Close()
	reg.RequireAuth = true
	reg.PageSize = 2

	ref, err := registry.ParseReference(reg.Host() + "/library/golang:1.26.10")
	require.NoError(t, err)
	c := newClient(nil)

	tags, err := c.Tags(context.Background(), ref)
	require.NoError(t, err)
	assert.ElementsMatch(
		t,
		[]string{"1.26.10", "1.26.11", "1.27.3", "1.27.3-alpine", "latest"},
		tags,
	)

	d, err := c.Digest(context.Background(), ref, "1.27.3")
	require.NoError(t, err)
	assert.Equal(t, testserver.Digest("library/golang", "1.27.3"), d)

	_, err = c.Digest(context.Background(), ref, "9.9.9")
	require.Error(t, err)
	assert.True(t, registry.IsNotFound(err))
}

func TestDigestWithoutHeaderHashesManifest(t *testing.T) {
	reg := testserver.NewRegistry(map[string][]string{"library/ubuntu": {"24.04"}})
	defer reg.Close()
	reg.OmitDigestHeader = true

	ref, _ := registry.ParseReference(reg.Host() + "/library/ubuntu:24.04")
	d, err := newClient(nil).Digest(context.Background(), ref, "24.04")
	require.NoError(t, err)
	assert.Equal(t, testserver.Digest("library/ubuntu", "24.04"), d)
}

func TestRetriesRateLimit(t *testing.T) {
	reg := testserver.NewRegistry(map[string][]string{"library/ubuntu": {"24.04"}})
	defer reg.Close()
	reg.FailFirst = 2

	ref, _ := registry.ParseReference(reg.Host() + "/library/ubuntu:24.04")
	c := newClient(nil)
	var warnings []string
	c.Warn = func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }
	d, err := c.Digest(context.Background(), ref, "24.04")
	require.NoError(t, err)
	assert.Equal(t, testserver.Digest("library/ubuntu", "24.04"), d)
	assert.GreaterOrEqual(t, len(reg.Requests), 3)
	require.Len(t, warnings, 2, "every retry is reported")
	assert.Contains(t, warnings[0], reg.Host()+": 429 Too Many Requests; retrying in ")
	assert.Contains(t, warnings[1], "(attempt 3 of 5)")
}

// A network error is retried once, not until MaxAttempts: each attempt can take the
// whole client timeout.
func TestNetworkErrorsRetryOnce(t *testing.T) {
	ref, _ := registry.ParseReference("127.0.0.1:1/library/ubuntu:24.04")
	c := newClient(nil)
	attempts := 0
	c.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("dial tcp: i/o timeout")
	})}
	var warnings []string
	c.Warn = func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }
	_, err := c.Digest(context.Background(), ref, "24.04")
	require.Error(t, err)
	assert.Equal(t, 2, attempts)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "i/o timeout; retrying in 1s (attempt 2 of 5)")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	reg := testserver.NewRegistry(map[string][]string{"library/ubuntu": {"24.04"}})
	defer reg.Close()
	reg.FailFirst = 100

	ref, _ := registry.ParseReference(reg.Host() + "/library/ubuntu:24.04")
	c := newClient(nil)
	c.MaxAttempts = 3
	_, err := c.Tags(context.Background(), ref)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "429")
}

func TestRejectedCredentialsFallBackToAnonymous(t *testing.T) {
	reg := testserver.NewRegistry(map[string][]string{"owner/tool": {"1.0.0", "1.1.0"}})
	defer reg.Close()
	reg.RequireAuth = true
	reg.RejectCredentials = true

	ref, _ := registry.ParseReference(reg.Host() + "/owner/tool:1.0.0")
	env := map[string]string{"DOCKER_CONFIG": t.TempDir()}
	// Give the fake registry host a credential via the docker config format.
	auth := base64.StdEncoding.EncodeToString([]byte("user:token-without-read-packages"))
	cfg := `{"auths":{"` + reg.Host() + `":{"auth":"` + auth + `"}}}`
	require.NoError(
		t,
		os.WriteFile(filepath.Join(env["DOCKER_CONFIG"], "config.json"), []byte(cfg), 0o600),
	)
	creds := registry.LoadCredentials(func(k string) string { return env[k] })
	_, ok := creds.Lookup(reg.Host())
	require.True(t, ok)

	c := newClient(creds)
	tags, err := c.Tags(context.Background(), ref)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"1.0.0", "1.1.0"}, tags)
	d, err := c.Digest(context.Background(), ref, "1.1.0")
	require.NoError(t, err)
	assert.Equal(t, testserver.Digest("owner/tool", "1.1.0"), d)
}

func TestTagsRetriesWithoutPageSize(t *testing.T) {
	reg := testserver.NewRegistry(map[string][]string{"library/alpine": {"3.21", "3.22"}})
	defer reg.Close()
	reg.MaxPageSize = 100

	ref, _ := registry.ParseReference(reg.Host() + "/library/alpine:3.21")
	tags, err := newClient(nil).Tags(context.Background(), ref)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"3.21", "3.22"}, tags)
}

func TestLoadCredentials(t *testing.T) {
	dir := t.TempDir()
	auth := base64.StdEncoding.EncodeToString([]byte("me:secret"))
	cfg := `{"auths":{"https://index.docker.io/v1/":{"auth":"` + auth + `"},` +
		`"quay.io":{"username":"q","password":"p"},"ghcr.io":{"auth":"` + auth + `"}}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600))

	env := map[string]string{"DOCKER_CONFIG": dir, "GITHUB_TOKEN": "ghp_x"}
	creds := registry.LoadCredentials(func(k string) string { return env[k] })

	c, ok := creds.Lookup("docker.io")
	require.True(t, ok)
	assert.Equal(t, registry.Credential{Username: "me", Password: "secret"}, c)

	c, ok = creds.Lookup("quay.io")
	require.True(t, ok)
	assert.Equal(t, "q", c.Username)

	// Environment variables win over the docker config file.
	c, ok = creds.Lookup("ghcr.io")
	require.True(t, ok)
	assert.Equal(t, "ghp_x", c.Password)

	_, ok = creds.Lookup("example.com")
	assert.False(t, ok)

	env["DOCKERHUB_USERNAME"], env["DOCKERHUB_TOKEN"] = "hubuser", "dckr_pat"
	creds = registry.LoadCredentials(func(k string) string { return env[k] })
	c, _ = creds.Lookup("docker.io")
	assert.Equal(t, "hubuser", c.Username)
}
