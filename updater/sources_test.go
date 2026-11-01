// SPDX-License-Identifier: MIT

package updater_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/esacteksab/gh-anchor/datasource"
	"github.com/esacteksab/gh-anchor/internal/testserver"
	"github.com/esacteksab/gh-anchor/registry"
	"github.com/esacteksab/gh-anchor/updater"
)

func TestLiveSourcesMemoizeAndCreateGitHubLazily(t *testing.T) {
	reg := testserver.NewRegistry(map[string][]string{"library/golang": {"1.26.10", "1.27.3"}})
	defer reg.Close()
	npm := testserver.NewNPM(
		map[string]testserver.NPMPackage{"pnpm": {Versions: []string{"12.16.1"}}},
	)
	defer npm.Close()
	node := testserver.NewNode([]testserver.NodeRelease{{Version: "v22.24.0", LTS: "Jod"}})
	defer node.Close()
	gh := testserver.NewGitHub(map[string]testserver.GitHubRepo{
		"o/r": {
			DefaultBranch: "main",
			Branches:      map[string]string{"main": zshHead},
			Tags:          []testserver.GitHubTag{{Name: "v1.0.0", SHA: zshV071}},
			Releases:      []testserver.GitHubRelease{{Tag: "v1.0.0"}},
		},
	})
	defer gh.Close()

	ghCreated := 0
	s := &updater.LiveSources{
		Registry: registry.NewClient(nil, "test"),
		NPM:      &datasource.NPM{BaseURL: npm.URL, HTTP: http.DefaultClient},
		Node:     &datasource.Node{BaseURL: node.URL, HTTP: http.DefaultClient},
		NewGitHub: func(context.Context) (*datasource.GitHub, error) {
			ghCreated++
			return datasource.NewGitHub(http.DefaultClient, "", gh.URL, "test")
		},
	}
	ctx := context.Background()
	ref, err := registry.ParseReference(reg.Host() + "/library/golang:1.26.10")
	require.NoError(t, err)

	for range 2 {
		tags, err := s.ImageTags(ctx, ref)
		require.NoError(t, err)
		assert.Equal(t, []string{"1.26.10", "1.27.3"}, tags)
		d, err := s.ImageDigest(ctx, ref, "1.27.3")
		require.NoError(t, err)
		assert.Equal(t, testserver.Digest("library/golang", "1.27.3"), d)

		p, err := s.NPMPackage(ctx, "pnpm")
		require.NoError(t, err)
		assert.Equal(t, []string{"12.16.1"}, p.Versions)
		rel, err := s.NodeReleases(ctx)
		require.NoError(t, err)
		assert.Len(t, rel, 1)
	}
	assert.Len(t, reg.Requests, 2, "registry lookups are memoized")
	assert.Equal(t, 0, ghCreated, "GitHub client is only created when needed")

	tags, err := s.GitHubTags(ctx, "o", "r")
	require.NoError(t, err)
	assert.Equal(t, []datasource.Tag{{Name: "v1.0.0", SHA: zshV071}}, tags)
	rels, err := s.GitHubReleases(ctx, "o", "r")
	require.NoError(t, err)
	assert.Equal(t, []string{"v1.0.0"}, rels)
	sha, branch, err := s.GitHubBranchHead(ctx, "o", "r", "")
	require.NoError(t, err)
	assert.Equal(t, zshHead, sha)
	assert.Equal(t, "main", branch)
	sha, err = s.GitHubResolveRef(ctx, "o", "r", "v1.0.0")
	require.NoError(t, err)
	assert.Equal(t, zshV071, sha)
	assert.Equal(t, 1, ghCreated)
}
