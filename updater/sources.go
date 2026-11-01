// SPDX-License-Identifier: MIT

package updater

import (
	"context"
	"sync"

	"github.com/esacteksab/gh-anchor/datasource"
	"github.com/esacteksab/gh-anchor/registry"
)

// LiveSources implements Sources against real registries and APIs, memoizing every
// lookup for the lifetime of one run.
type LiveSources struct {
	Registry *registry.Client
	NPM      *datasource.NPM
	Node     *datasource.Node
	// NewGitHub creates the GitHub client on first use, so runs that never touch
	// GitHub make no GitHub API calls.
	NewGitHub func(ctx context.Context) (*datasource.GitHub, error)

	mu       sync.Mutex
	gh       *datasource.GitHub
	ghErr    error
	ghOnce   sync.Once
	tags     map[string][]string
	digests  map[string]string
	packages map[string]datasource.Package
	node     []datasource.NodeRelease
	ghTags   map[string][]datasource.Tag
	ghRels   map[string][]string
}

func memo[V any](s *LiveSources, m *map[string]V, key string, fetch func() (V, error)) (V, error) {
	s.mu.Lock()
	if *m == nil {
		*m = map[string]V{}
	}
	if v, ok := (*m)[key]; ok {
		s.mu.Unlock()
		return v, nil
	}
	s.mu.Unlock()
	v, err := fetch()
	if err != nil {
		return v, err
	}
	s.mu.Lock()
	(*m)[key] = v
	s.mu.Unlock()
	return v, nil
}

func (s *LiveSources) github(ctx context.Context) (*datasource.GitHub, error) {
	s.ghOnce.Do(func() { s.gh, s.ghErr = s.NewGitHub(ctx) })
	return s.gh, s.ghErr
}

// ImageTags implements Sources.
func (s *LiveSources) ImageTags(ctx context.Context, ref registry.Reference) ([]string, error) {
	return memo(s, &s.tags, ref.Repository(), func() ([]string, error) {
		return s.Registry.Tags(ctx, ref)
	})
}

// ImageDigest implements Sources.
func (s *LiveSources) ImageDigest(
	ctx context.Context,
	ref registry.Reference,
	tag string,
) (string, error) {
	return memo(s, &s.digests, ref.Repository()+":"+tag, func() (string, error) {
		return s.Registry.Digest(ctx, ref, tag)
	})
}

// NPMPackage implements Sources.
func (s *LiveSources) NPMPackage(ctx context.Context, name string) (datasource.Package, error) {
	return memo(s, &s.packages, name, func() (datasource.Package, error) {
		return s.NPM.Package(ctx, name)
	})
}

// NodeReleases implements Sources.
func (s *LiveSources) NodeReleases(ctx context.Context) ([]datasource.NodeRelease, error) {
	s.mu.Lock()
	cached := s.node
	s.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	rel, err := s.Node.Releases(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.node = rel
	s.mu.Unlock()
	return rel, nil
}

// GitHubTags implements Sources.
func (s *LiveSources) GitHubTags(
	ctx context.Context,
	owner, repo string,
) ([]datasource.Tag, error) {
	return memo(s, &s.ghTags, owner+"/"+repo, func() ([]datasource.Tag, error) {
		gh, err := s.github(ctx)
		if err != nil {
			return nil, err
		}
		return gh.Tags(ctx, owner, repo)
	})
}

// GitHubReleases implements Sources.
func (s *LiveSources) GitHubReleases(ctx context.Context, owner, repo string) ([]string, error) {
	return memo(s, &s.ghRels, owner+"/"+repo, func() ([]string, error) {
		gh, err := s.github(ctx)
		if err != nil {
			return nil, err
		}
		return gh.Releases(ctx, owner, repo)
	})
}

// GitHubBranchHead implements Sources.
func (s *LiveSources) GitHubBranchHead(
	ctx context.Context,
	owner, repo, branch string,
) (string, string, error) {
	gh, err := s.github(ctx)
	if err != nil {
		return "", "", err
	}
	return gh.BranchHead(ctx, owner, repo, branch)
}

// GitHubResolveRef implements Sources.
func (s *LiveSources) GitHubResolveRef(
	ctx context.Context,
	owner, repo, ref string,
) (string, error) {
	gh, err := s.github(ctx)
	if err != nil {
		return "", err
	}
	return gh.ResolveRef(ctx, owner, repo, ref)
}
