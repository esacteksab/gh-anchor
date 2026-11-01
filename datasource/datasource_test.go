// SPDX-License-Identifier: MIT

package datasource_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/esacteksab/gh-anchor/datasource"
	"github.com/esacteksab/gh-anchor/internal/testserver"
)

func TestNPM(t *testing.T) {
	srv := testserver.NewNPM(map[string]testserver.NPMPackage{
		"pnpm": {
			Versions:   []string{"12.16.1", "12.17.2", "13.1.0"},
			Deprecated: []string{"12.17.0"},
			DistTags:   map[string]string{"latest": "13.1.0"},
		},
		"@scope/pkg": {Versions: []string{"1.0.0"}, DistTags: map[string]string{"latest": "1.0.0"}},
	})
	defer srv.Close()
	n := &datasource.NPM{BaseURL: srv.URL, HTTP: http.DefaultClient}

	p, err := n.Package(context.Background(), "pnpm")
	require.NoError(t, err)
	assert.Equal(t, []string{"12.16.1", "12.17.2", "13.1.0"}, p.Versions)
	assert.Equal(t, "13.1.0", p.DistTags["latest"])

	p, err = n.Package(context.Background(), "@scope/pkg")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.0.0"}, p.Versions)

	_, err = n.Package(context.Background(), "nope")
	require.Error(t, err)
}

func TestNode(t *testing.T) {
	srv := testserver.NewNode([]testserver.NodeRelease{
		{Version: "v26.1.0"},
		{Version: "v24.12.0", LTS: "Krypton"},
		{Version: "v22.23.4", LTS: "Jod"},
	})
	defer srv.Close()
	n := &datasource.Node{BaseURL: srv.URL, HTTP: http.DefaultClient}

	rel, err := n.Releases(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []datasource.NodeRelease{
		{Version: "v26.1.0", LTS: false},
		{Version: "v24.12.0", LTS: true},
		{Version: "v22.23.4", LTS: true},
	}, rel)
}

const (
	shaHead = "1111111111111111111111111111111111111111"
	shaV071 = "85919cd1ffa7d2d5412f6d3fe437ebdbeeec4fc5"
	shaV070 = "2222222222222222222222222222222222222222"
	shaV080 = "3333333333333333333333333333333333333333"
)

func newGitHub(t *testing.T) *datasource.GitHub {
	t.Helper()
	srv := testserver.NewGitHub(map[string]testserver.GitHubRepo{
		"zsh-users/zsh-autosuggestions": {
			DefaultBranch: "master",
			Branches:      map[string]string{"master": shaHead, "develop": shaV080},
			Tags: []testserver.GitHubTag{
				{Name: "v0.8.0", SHA: shaV080, Annotated: true},
				{Name: "v0.7.1", SHA: shaV071},
				{Name: "v0.7.0", SHA: shaV070},
			},
			Releases: []testserver.GitHubRelease{
				{Tag: "v0.9.0-beta", Prerelease: true},
				{Tag: "v0.8.0"},
				{Tag: "v0.8.1", Draft: true},
			},
		},
	})
	t.Cleanup(srv.Close)
	gh, err := datasource.NewGitHub(http.DefaultClient, "token", srv.URL, "test")
	require.NoError(t, err)
	return gh
}

func TestGitHub(t *testing.T) {
	ctx := context.Background()
	gh := newGitHub(t)
	const owner, repo = "zsh-users", "zsh-autosuggestions"

	assert.Equal(t, "authenticated", gh.AuthStatus(ctx))

	tags, err := gh.Tags(ctx, owner, repo)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]datasource.Tag{
			{Name: "v0.8.0", SHA: shaV080},
			{Name: "v0.7.1", SHA: shaV071},
			{Name: "v0.7.0", SHA: shaV070},
		},
		tags,
	)

	rels, err := gh.Releases(ctx, owner, repo)
	require.NoError(t, err)
	assert.Equal(t, []string{"v0.8.0"}, rels)

	sha, branch, err := gh.BranchHead(ctx, owner, repo, "")
	require.NoError(t, err)
	assert.Equal(t, shaHead, sha)
	assert.Equal(t, "master", branch)

	for ref, want := range map[string]string{
		"v0.7.1":  shaV071, // lightweight tag
		"v0.8.0":  shaV080, // annotated tag, peeled
		"develop": shaV080, // branch
		shaV070:   shaV070, // commit
	} {
		got, err := gh.ResolveRef(ctx, owner, repo, ref)
		require.NoError(t, err, ref)
		assert.Equal(t, want, got, ref)
	}
	_, err = gh.ResolveRef(ctx, owner, repo, "missing")
	require.Error(t, err)
}

func TestIsSHA(t *testing.T) {
	assert.True(t, datasource.IsSHA(shaV071))
	assert.False(t, datasource.IsSHA("85919cd"))
	assert.False(t, datasource.IsSHA("zz919cd1ffa7d2d5412f6d3fe437ebdbeeec4fc5"))
}
