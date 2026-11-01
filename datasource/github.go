// SPDX-License-Identifier: MIT

package datasource

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v91/github"
)

// SHALength is the length of a full Git SHA-1.
const SHALength = 40

const (
	authLimit      = 5000
	unAuthLimit    = 60
	perPage        = 100
	maxTagPages    = 5
	maxReleasePage = 3
)

// IsSHA reports whether s is a full 40-character hexadecimal commit SHA.
func IsSHA(s string) bool {
	if len(s) != SHALength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// GitHub wraps a go-github client with the lookups gh-anchor needs.
type GitHub struct {
	Client *github.Client
}

// NewGitHub creates a GitHub client on top of httpClient. token may be empty
// (unauthenticated, 60 requests/hour). baseURL may be empty for api.github.com.
func NewGitHub(httpClient *http.Client, token, baseURL, userAgent string) (*GitHub, error) {
	opts := []github.ClientOptionsFunc{github.WithHTTPClient(httpClient)}
	if token != "" {
		opts = append(opts, github.WithAuthToken(token))
	}
	if userAgent != "" {
		opts = append(opts, github.WithUserAgent(userAgent))
	}
	if baseURL != "" {
		if !strings.HasSuffix(baseURL, "/") {
			baseURL += "/"
		}
		opts = append(opts, github.WithURLs(&baseURL, nil))
	}
	c, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GitHub client: %w", err)
	}
	return &GitHub{Client: c}, nil
}

// AuthStatus returns "authenticated", "unauthenticated" or "unknown" based on the rate limit.
func (g *GitHub) AuthStatus(ctx context.Context) string {
	limits, _, err := g.Client.RateLimit.Get(ctx)
	if err != nil || limits == nil || limits.Core == nil {
		return "unknown"
	}
	switch {
	case limits.Core.Limit >= authLimit:
		return "authenticated"
	case limits.Core.Limit <= unAuthLimit:
		return "unauthenticated"
	default:
		return "unknown"
	}
}

// Tag is a Git tag and the commit it points to.
type Tag struct {
	Name string
	SHA  string
}

// Tags lists up to 500 of the repository's most recent tags.
func (g *GitHub) Tags(ctx context.Context, owner, repo string) ([]Tag, error) {
	var out []Tag
	opt := &github.ListOptions{PerPage: perPage}
	for range maxTagPages {
		tags, resp, err := g.Client.Repositories.ListTags(ctx, owner, repo, opt)
		if err != nil {
			return nil, fmt.Errorf("listing tags for %s/%s: %w", owner, repo, err)
		}
		for _, t := range tags {
			if t.Name == nil || t.Commit == nil || t.Commit.SHA == nil {
				continue
			}
			out = append(out, Tag{Name: *t.Name, SHA: *t.Commit.SHA})
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return out, nil
}

// Releases returns the tag names of published, non-prerelease releases.
func (g *GitHub) Releases(ctx context.Context, owner, repo string) ([]string, error) {
	var out []string
	opt := &github.ListOptions{PerPage: perPage}
	for range maxReleasePage {
		rels, resp, err := g.Client.Repositories.ListReleases(ctx, owner, repo, opt)
		if err != nil {
			return nil, fmt.Errorf("listing releases for %s/%s: %w", owner, repo, err)
		}
		for _, r := range rels {
			if r.GetDraft() || r.GetPrerelease() || r.GetTagName() == "" {
				continue
			}
			out = append(out, r.GetTagName())
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return out, nil
}

// BranchHead returns the head commit of branch, or of the default branch when branch is empty.
func (g *GitHub) BranchHead(
	ctx context.Context,
	owner, repo, branch string,
) (string, string, error) {
	if branch == "" {
		r, _, err := g.Client.Repositories.Get(ctx, owner, repo)
		if err != nil {
			return "", "", fmt.Errorf("getting %s/%s: %w", owner, repo, err)
		}
		branch = r.GetDefaultBranch()
		if branch == "" {
			return "", "", fmt.Errorf("%s/%s has no default branch", owner, repo)
		}
	}
	sha, found, err := g.resolveBranch(ctx, owner, repo, branch)
	if err != nil {
		return "", "", err
	}
	if !found {
		return "", "", fmt.Errorf("branch %q not found in %s/%s", branch, owner, repo)
	}
	return sha, branch, nil
}

// ResolveRef resolves a tag, branch or commit SHA to a full commit SHA.
// Tags are checked before branches, and annotated tags are peeled to their commit.
func (g *GitHub) ResolveRef(ctx context.Context, owner, repo, ref string) (string, error) {
	if owner == "" || repo == "" || ref == "" {
		return "", errors.New("owner, repo, and ref must not be empty")
	}
	if IsSHA(ref) {
		if _, resp, err := g.Client.Git.GetCommit(ctx, owner, repo, ref); err == nil {
			return strings.ToLower(ref), nil
		} else if !isNotFound(err, resp) {
			return "", fmt.Errorf("verifying commit %s in %s/%s: %w", ref, owner, repo, err)
		}
	}
	if sha, found, err := g.resolveTag(ctx, owner, repo, ref); err != nil {
		return "", err
	} else if found {
		return sha, nil
	}
	if sha, found, err := g.resolveBranch(ctx, owner, repo, ref); err != nil {
		return "", err
	} else if found {
		return sha, nil
	}
	return "", fmt.Errorf("reference '%s' not found as a tag or branch in %s/%s", ref, owner, repo)
}

func (g *GitHub) resolveTag(ctx context.Context, owner, repo, tag string) (string, bool, error) {
	ref, resp, err := g.Client.Git.GetRef(ctx, owner, repo, "refs/tags/"+tag)
	if err != nil {
		if isNotFound(err, resp) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("getting tag %s in %s/%s: %w", tag, owner, repo, err)
	}
	obj := ref.GetObject()
	switch obj.GetType() {
	case "commit":
		return obj.GetSHA(), true, nil
	case "tag": // annotated tag: peel to the commit
		t, _, err := g.Client.Git.GetTag(ctx, owner, repo, obj.GetSHA())
		if err != nil {
			return "", false, fmt.Errorf(
				"getting tag object %s in %s/%s: %w",
				obj.GetSHA(),
				owner,
				repo,
				err,
			)
		}
		if t.GetObject().GetType() != "commit" {
			return "", false, fmt.Errorf(
				"tag %s in %s/%s points to a %s, not a commit",
				tag,
				owner,
				repo,
				t.GetObject().GetType(),
			)
		}
		return t.GetObject().GetSHA(), true, nil
	default:
		return "", false, fmt.Errorf(
			"tag %s in %s/%s points to a %s",
			tag,
			owner,
			repo,
			obj.GetType(),
		)
	}
}

func (g *GitHub) resolveBranch(
	ctx context.Context,
	owner, repo, branch string,
) (string, bool, error) {
	ref, resp, err := g.Client.Git.GetRef(ctx, owner, repo, "refs/heads/"+branch)
	if err != nil {
		if isNotFound(err, resp) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("getting branch %s in %s/%s: %w", branch, owner, repo, err)
	}
	if ref.GetObject().GetType() != "commit" {
		return "", false, fmt.Errorf(
			"branch %s in %s/%s points to a %s",
			branch,
			owner,
			repo,
			ref.GetObject().GetType(),
		)
	}
	return ref.GetObject().GetSHA(), true, nil
}

func isNotFound(err error, resp *github.Response) bool {
	var er *github.ErrorResponse
	if errors.As(err, &er) && er.Response != nil && er.Response.StatusCode == http.StatusNotFound {
		return true
	}
	return resp != nil && resp.StatusCode == http.StatusNotFound
}
