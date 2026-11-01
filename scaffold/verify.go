// SPDX-License-Identifier: MIT

package scaffold

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/esacteksab/gh-anchor/config"
	"github.com/esacteksab/gh-anchor/datasource"
	"github.com/esacteksab/gh-anchor/registry"
	"github.com/esacteksab/gh-anchor/updater"
	"github.com/esacteksab/gh-anchor/versioning"
)

// Verify checks every suggestion against the registries and refines it:
//
//   - an npm package, Docker image or GitHub repository that doesn't exist rejects
//     the ARG suggestion (it is written commented out and not selected);
//   - a commit SHA that is a tagged release switches to track = "tags";
//   - a github-releases ARG that isn't a release switches to github-tags.
//
// When a lookup fails for any other reason (network, rate limit) the offline
// suggestion is kept and a note says it wasn't verified.
func (p *Plan) Verify(ctx context.Context, src updater.Sources) {
	v := verifier{ctx: ctx, src: src}
	for i := range p.Suggestions {
		s := &p.Suggestions[i]
		switch s.Section {
		case SectionArgs:
			v.arg(s)
		case SectionImages:
			v.image(s)
		case SectionPackages:
			v.pkg(s)
		}
	}
}

type verifier struct {
	ctx context.Context
	src updater.Sources
}

// fail records a failed lookup. A "not found" rejects ARG suggestions.
func (v verifier) fail(s *Suggestion, what string, err error) {
	if datasource.IsNotFound(err) {
		s.Notes = append(s.Notes, what+" not found")
		if s.Section == SectionArgs {
			s.Rejected = true
			s.Selected = false
		}
		return
	}
	s.Notes = append(
		s.Notes,
		fmt.Sprintf("not verified (%s); using the offline default", shortErr(err)),
	)
}

func (v verifier) arg(s *Suggestion) {
	switch s.Arg.Datasource {
	case config.DatasourceNodeVersion:
		v.node(s)
	case config.DatasourceNPM:
		pkg, err := v.src.NPMPackage(v.ctx, s.Arg.Package)
		if err != nil {
			v.fail(s, fmt.Sprintf("npm package %q", s.Arg.Package), err)
			return
		}
		v.found(s, pkg.Versions, false, "a version of npm package "+s.Arg.Package)
	case config.DatasourceDocker:
		ref, err := registry.ParseReference(s.Arg.Image)
		if err != nil {
			return
		}
		tags, err := v.src.ImageTags(v.ctx, ref)
		if err != nil {
			v.fail(s, "image "+s.Arg.Image, err)
			return
		}
		v.found(s, tags, false, "a tag of "+s.Arg.Image)
	case config.DatasourceGitHubReleases, config.DatasourceGitHubTags:
		v.github(s)
	case config.DatasourceGitHubCommits:
		v.commit(s)
	}
}

// found notes whether the current value is among versions.
func (v verifier) found(s *Suggestion, versions []string, ignorePrefix bool, what string) {
	if versioning.Contains(s.Current, versions, ignorePrefix) {
		s.Notes = append(s.Notes, fmt.Sprintf("verified: %s is %s", s.Current, what))
		return
	}
	s.Notes = append(
		s.Notes,
		fmt.Sprintf("%s is not published as %s; check the value", s.Current, what),
	)
}

func (v verifier) node(s *Suggestion) {
	releases, err := v.src.NodeReleases(v.ctx)
	if err != nil {
		v.fail(s, "Node.js release index", err)
		return
	}
	for _, r := range releases {
		if strings.TrimPrefix(r.Version, "v") == strings.TrimPrefix(s.Current, "v") {
			kind := "a Current release"
			if r.LTS {
				kind = "an LTS release"
			}
			s.Notes = append(s.Notes, fmt.Sprintf("verified: Node.js %s is %s", s.Current, kind))
			return
		}
	}
	s.Notes = append(s.Notes, s.Current+" is not in the Node.js release index; check the value")
}

// github verifies a release/tag ARG, preferring releases and falling back to tags.
func (v verifier) github(s *Suggestion) {
	owner, repo := s.Arg.Owner(), s.Arg.RepoName()
	releases, err := v.src.GitHubReleases(v.ctx, owner, repo)
	if err != nil {
		v.fail(s, "GitHub repository "+s.Arg.Repo, err)
		return
	}
	if versioning.Contains(s.Current, releases, true) {
		s.Arg.Datasource = config.DatasourceGitHubReleases
		s.Notes = append(
			s.Notes,
			fmt.Sprintf("verified: %s is a release of %s", s.Current, s.Arg.Repo),
		)
		return
	}
	tags, err := v.src.GitHubTags(v.ctx, owner, repo)
	if err != nil {
		v.fail(s, "tags of "+s.Arg.Repo, err)
		return
	}
	if versioning.Contains(s.Current, tagNames(tags), true) {
		s.Arg.Datasource = config.DatasourceGitHubTags
		s.Notes = append(
			s.Notes,
			fmt.Sprintf("verified: %s is a tag (not a release) of %s", s.Current, s.Arg.Repo),
		)
		return
	}
	s.Notes = append(
		s.Notes,
		fmt.Sprintf(
			"%s is neither a release nor a tag of %s; check the value",
			s.Current,
			s.Arg.Repo,
		),
	)
}

// commit picks track = "tags" when the SHA is a tagged release, else follows the branch.
func (v verifier) commit(s *Suggestion) {
	owner, repo := s.Arg.Owner(), s.Arg.RepoName()
	tags, err := v.src.GitHubTags(v.ctx, owner, repo)
	if err != nil {
		v.fail(s, "GitHub repository "+s.Arg.Repo, err)
		return
	}
	for _, t := range tags {
		if strings.EqualFold(t.SHA, s.Current) {
			s.Arg.Track = config.TrackTags
			s.Notes = append(
				s.Notes,
				fmt.Sprintf(
					"verified: %s is tag %s, so track = \"tags\"",
					short(s.Current),
					t.Name,
				),
			)
			return
		}
	}
	head, branch, err := v.src.GitHubBranchHead(v.ctx, owner, repo, "")
	if err != nil {
		v.fail(s, "default branch of "+s.Arg.Repo, err)
		return
	}
	s.Arg.Track = config.TrackBranch
	if strings.EqualFold(head, s.Current) {
		s.Notes = append(
			s.Notes,
			fmt.Sprintf("verified: %s is the head of %s and not tagged, so track = \"branch\"",
				short(s.Current), branch),
		)
		return
	}
	s.Notes = append(
		s.Notes,
		fmt.Sprintf("%s is not tagged and is not the head of %s (now %s); following %s",
			short(s.Current), branch, short(head), branch),
	)
}

func (v verifier) image(s *Suggestion) {
	ref, err := registry.ParseReference(s.Current)
	if err != nil {
		return
	}
	tag := ref.Tag
	if tag == "" {
		if ref.Digest != "" {
			s.Notes = append(
				s.Notes,
				"pinned by digest only: gh-anchor adds the tag that has this digest",
			)
			return
		}
		tag = "latest"
	}
	if _, err := v.src.ImageDigest(v.ctx, ref, tag); err != nil {
		v.fail(s, fmt.Sprintf("tag %s of %s", tag, ref.Name), err)
	}
}

// pkg checks that the package version is published and records its hash.
func (v verifier) pkg(s *Suggestion) {
	p, err := v.src.NPMPackage(v.ctx, s.Name)
	if err != nil {
		v.fail(s, fmt.Sprintf("npm package %q", s.Name), err)
		if datasource.IsNotFound(err) {
			s.Selected = false
		}
		return
	}
	version := s.Package.Version
	published, ok := p.Hashes[version]
	switch {
	case !ok && !slices.Contains(p.Versions, version):
		// A dist-tag such as "latest": the first update pins a version.
		if tagged, isTag := p.DistTags[version]; isTag {
			s.Notes = append(
				s.Notes,
				fmt.Sprintf(
					"%s is a dist-tag (now %s); the first run pins a version",
					version,
					tagged,
				),
			)
			return
		}
		s.Notes = append(
			s.Notes,
			fmt.Sprintf("%s is not a published version of %s; check the value", version, s.Name),
		)
	case s.Package.Hash == "" && published != "":
		s.Package.Hash = published
		s.Notes = append(
			s.Notes,
			fmt.Sprintf("verified: %s@%s is published; hash from npm", s.Name, version),
		)
	case published != "" && published != s.Package.Hash:
		s.Notes = append(
			s.Notes,
			fmt.Sprintf("the Dockerfile's hash differs from the one npm publishes for %s@%s (%s); "+
				"gh-anchor reports an error until they match", s.Name, version, updater.Shorten(published)),
		)
	default:
		s.Notes = append(s.Notes, fmt.Sprintf("verified: %s@%s is published", s.Name, version))
	}
}

func tagNames(tags []datasource.Tag) []string {
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}
	return names
}

func short(sha string) string {
	const n = 12
	if len(sha) > n {
		return sha[:n]
	}
	return sha
}

// shortErr returns the first line of err, trimmed for a comment.
func shortErr(err error) string {
	const maxLen = 100
	msg, _, _ := strings.Cut(err.Error(), "\n")
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "…"
	}
	return msg
}
