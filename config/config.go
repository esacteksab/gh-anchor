// SPDX-License-Identifier: MIT

// Package config loads .gh-anchor.toml, which tells gh-anchor where the value of each
// ARG comes from and lets you tune how images and inline packages are handled.
//
//	[args.NODE_VERSION]
//	  datasource = "node-version"
//	  lts        = true
//
//	[args.NPM_VERSION]
//	  datasource = "npm"
//	  package    = "npm"
//
//	[args.ZSH_AUTOSUGGESTIONS_SHA]
//	  datasource = "github-commits"
//	  repo       = "zsh-users/zsh-autosuggestions"
//	  track      = "branch"
//
//	[images.python]           # the stage `FROM ubuntu:24.04@sha256:... AS python`
//	  image     = "ubuntu"
//	  tag       = "24.04"
//	  digest    = "sha256:..."
//	  fixed-tag = false
//	  lts       = true
//	  skip      = false
//
//	[packages.pnpm]
//	  version = "12.16.1"
//	  hash    = "sha512.091d..."
//	  skip    = false
//
// When a config file exists it is the complete list of what gh-anchor updates:
// images and packages need a table, just like ARGs. tag, digest, version and hash
// record what the Dockerfile uses; gh-anchor checks them before changing anything
// and keeps them up to date.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/esacteksab/gh-anchor/registry"
)

// DefaultFile is the config file looked up in the current directory.
const DefaultFile = ".gh-anchor.toml"

// Datasource names accepted in `args.<NAME>.datasource`.
const (
	DatasourceNPM            = "npm"
	DatasourceNodeVersion    = "node-version"
	DatasourceDocker         = "docker"
	DatasourceGitHubTags     = "github-tags"
	DatasourceGitHubReleases = "github-releases"
	DatasourceGitHubCommits  = "github-commits"
)

// Track values for the github-commits datasource.
const (
	TrackBranch = "branch"
	TrackTags   = "tags"
)

// ubuntuLTSPattern matches Ubuntu LTS release tags: an even year and the April release
// (20.04, 22.04, 24.04, 26.04).
var ubuntuLTSPattern = regexp.MustCompile(`^\d*[02468]\.04$`)

// Arg describes where an ARG's value comes from.
type Arg struct {
	Datasource string `toml:"datasource"`
	Package    string `toml:"package"` // npm package name (npm)
	Repo       string `toml:"repo"`    // owner/repo (github-*)
	Image      string `toml:"image"`   // image repository (docker)
	Track      string `toml:"track"`   // branch (default) or tags (github-commits)
	Branch     string `toml:"branch"`  // branch to follow; default is the repo's default branch (github-commits)
	LTS        bool   `toml:"lts"`     // only consider LTS releases (node-version)
	Target     Target `toml:"target"`  // version line to move to and stay on, e.g. "24"
	Bump       bool   `toml:"bump"`    // allow new major versions with --update
	Skip       bool   `toml:"skip"`    // leave this ARG alone
}

// Owner returns the owner part of Repo.
func (a Arg) Owner() string { o, _, _ := strings.Cut(a.Repo, "/"); return o }

// RepoName returns the repository part of Repo.
func (a Arg) RepoName() string { _, r, _ := strings.Cut(a.Repo, "/"); return r }

// Image describes an image in FROM or COPY --from. Its table key is the build stage
// name (or "final" / "stage-N" for unnamed stages) for FROM, and the image repository
// for COPY --from.
type Image struct {
	Image      string `toml:"image"`       // image repository, e.g. "ubuntu" or "ghcr.io/owner/tool"
	Tag        string `toml:"tag"`         // recorded tag
	Digest     string `toml:"digest"`      // recorded digest, e.g. "sha256:..."
	FixedTag   bool   `toml:"fixed-tag"`   // never change the tag; only refresh its digest
	LTS        bool   `toml:"lts"`         // only move to LTS tags
	LTSPattern string `toml:"lts-pattern"` // regexp matching LTS tags; built in for ubuntu
	Target     Target `toml:"target"`      // version line to move to and stay on, e.g. "26.04"
	Bump       bool   `toml:"bump"`        // allow new major versions (and a new FROM tag) with --update
	Skip       bool   `toml:"skip"`        // leave this image alone

	lts *regexp.Regexp
}

// IsLTS reports whether tag is an LTS tag for this image. It is only meaningful when LTS is set.
func (i Image) IsLTS(tag string) bool {
	return i.lts != nil && i.lts.MatchString(tag)
}

// Package describes an inline package@version in RUN. Its table key is the npm
// package name.
type Package struct {
	Version string `toml:"version"` // recorded version
	Hash    string `toml:"hash"`    // recorded sha512 of the package, as "sha512.<hex>" (the corepack format)
	Target  Target `toml:"target"`  // version line to move to and stay on, e.g. "10"
	Bump    bool   `toml:"bump"`    // allow new major versions with --update
	Skip    bool   `toml:"skip"`    // leave this package alone
}

// Config is the parsed .gh-anchor.toml.
type Config struct {
	Args     map[string]Arg     `toml:"args"`
	Images   map[string]Image   `toml:"images"`
	Packages map[string]Package `toml:"packages"`

	// Found reports whether a config file was read.
	Found bool `toml:"-"`
	// Path is the file the config was read from, if any.
	Path string `toml:"-"`

	// images with their compiled LTS patterns, keyed like Images.
	images map[string]Image
}

// Load reads the config at path. If path is the default and does not exist, an empty
// config is returned; an explicitly requested file must exist.
func Load(path string, explicit bool) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is provided by the user on purpose
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return Parse(nil)
		}
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	cfg.Found = true
	cfg.Path = path
	return cfg, nil
}

// Parse parses and validates config data.
func Parse(data []byte) (*Config, error) {
	cfg := &Config{}
	md, err := toml.Decode(string(data), cfg)
	if err != nil {
		return nil, err
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return nil, fmt.Errorf("unknown key(s): %s", strings.Join(keys, ", "))
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var errs []error
	for _, name := range sortedKeys(c.Args) {
		if err := validateArg(c.Args[name]); err != nil {
			errs = append(errs, fmt.Errorf("args.%s: %w", name, err))
		}
	}
	c.images = map[string]Image{}
	for _, name := range sortedKeys(c.Images) {
		img, err := prepareImage(c.Images[name])
		if err != nil {
			errs = append(errs, fmt.Errorf("images.%q: %w", name, err))
			continue
		}
		c.images[name] = img
	}
	for _, name := range sortedKeys(c.Packages) {
		if h := c.Packages[name].Hash; h != "" && !packageHashRe.MatchString(h) {
			errs = append(
				errs,
				fmt.Errorf("packages.%q: hash must look like \"sha512.<hex>\" (got %q)", name, h),
			)
		}
		if err := c.Packages[name].Target.validate(); err != nil {
			errs = append(errs, fmt.Errorf("packages.%q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// packageHashRe matches a recorded package hash in corepack's format.
var packageHashRe = regexp.MustCompile(`^sha(1|224|256|512)\.[0-9a-f]+$`)

// digestRe matches a recorded image digest.
var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// prepareImage validates an image entry and compiles its LTS pattern.
func prepareImage(img Image) (Image, error) {
	if img.Image == "" {
		if img.Skip {
			return img, nil
		}
		return img, errors.New(`image is required, e.g. image = "ubuntu"`)
	}
	ref, err := registry.ParseReference(img.Image)
	if err != nil {
		return img, err
	}
	if ref.Tag != "" || ref.Digest != "" {
		return img, fmt.Errorf(
			"image must not include a tag or digest (use tag and digest): %q",
			img.Image,
		)
	}
	if err := validateImageRecord(img); err != nil {
		return img, err
	}
	switch {
	case img.LTSPattern != "":
		re, err := regexp.Compile(img.LTSPattern)
		if err != nil {
			return img, fmt.Errorf("lts-pattern: %w", err)
		}
		img.lts = re
	case img.LTS && isUbuntu(ref):
		img.lts = ubuntuLTSPattern
	case img.LTS:
		return img, errors.New("lts = true needs lts-pattern for images other than ubuntu " +
			`(e.g. lts-pattern = '^\d+\.\d+\.\d+$')`)
	}
	if img.LTSPattern != "" && !img.LTS {
		return img, errors.New("lts-pattern is set but lts is not true")
	}
	return img, nil
}

// validateImageRecord checks the recorded digest and the target.
func validateImageRecord(img Image) error {
	if img.Digest != "" && !digestRe.MatchString(img.Digest) {
		return fmt.Errorf("digest must look like \"sha256:<64 hex>\" (got %q)", img.Digest)
	}
	if err := img.Target.validate(); err != nil {
		return err
	}
	if img.Target != "" && img.FixedTag {
		return errors.New("fixed-tag and target can't both be set: fixed-tag never changes the tag")
	}
	return nil
}

// isUbuntu reports whether ref is an Ubuntu image (docker.io/library/ubuntu or a mirror
// whose repository ends in /ubuntu).
func isUbuntu(ref registry.Reference) bool {
	return ref.Repo == "ubuntu" || strings.HasSuffix(ref.Repo, "/ubuntu")
}

// datasourceNames lists the accepted datasources, for error messages.
var datasourceNames = []string{
	DatasourceNodeVersion, DatasourceNPM, DatasourceDocker,
	DatasourceGitHubTags, DatasourceGitHubReleases, DatasourceGitHubCommits,
}

// validators check the options each datasource needs.
var validators = map[string]func(Arg) error{
	DatasourceNPM: func(a Arg) error {
		if a.Package == "" {
			return errors.New(`datasource "npm" needs package`)
		}
		return nil
	},
	DatasourceNodeVersion: func(Arg) error { return nil },
	DatasourceDocker: func(a Arg) error {
		if a.Image == "" {
			return errors.New(`datasource "docker" needs image`)
		}
		_, err := registry.ParseReference(a.Image)
		return err
	},
	DatasourceGitHubTags:     validateRepo,
	DatasourceGitHubReleases: validateRepo,
	DatasourceGitHubCommits: func(a Arg) error {
		if a.Track != "" && a.Track != TrackBranch && a.Track != TrackTags {
			return fmt.Errorf("track must be %q or %q (got %q)", TrackBranch, TrackTags, a.Track)
		}
		return validateRepo(a)
	},
}

func validateRepo(a Arg) error {
	if o, r, ok := strings.Cut(a.Repo, "/"); !ok || o == "" || r == "" || strings.Contains(r, "/") {
		return fmt.Errorf(
			"datasource %q needs repo = \"owner/name\" (got %q)",
			a.Datasource,
			a.Repo,
		)
	}
	return nil
}

func validateArg(a Arg) error {
	if a.Skip {
		return nil
	}
	if err := a.Target.validate(); err != nil {
		return err
	}
	if a.Target != "" && a.Datasource == DatasourceGitHubCommits && a.Track != TrackTags {
		return errors.New(`target needs track = "tags": a branch has no version line`)
	}
	if a.Datasource == "" {
		return errors.New("datasource is required")
	}
	validate, ok := validators[a.Datasource]
	if !ok {
		return fmt.Errorf(
			"unknown datasource %q (want one of %s)",
			a.Datasource,
			strings.Join(datasourceNames, ", "),
		)
	}
	return validate(a)
}

// Sections of the config. Image and package tables record what the Dockerfile uses;
// ARG tables say where an ARG's versions come from.
const (
	SectionImages   = "images"
	SectionPackages = "packages"
	SectionArgs     = "args"
)

// Recorded returns a recorded value (tag or digest of an image, version or hash of a
// package) and whether the table and field exist.
func (c *Config) Recorded(section, key, field string) (string, bool) {
	switch section {
	case SectionImages:
		img, ok := c.Images[key]
		switch {
		case !ok:
		case field == "image":
			return img.Image, true
		case field == "tag":
			return img.Tag, true
		case field == "digest":
			return img.Digest, true
		}
	case SectionPackages:
		p, ok := c.Packages[key]
		switch {
		case !ok:
		case field == "version":
			return p.Version, true
		case field == "hash":
			return p.Hash, true
		}
	}
	return "", false
}

// Empty reports whether the config has no tables at all.
func (c *Config) Empty() bool {
	return len(c.Args) == 0 && len(c.Images) == 0 && len(c.Packages) == 0
}

// ImageFor returns the table for an image key (stage name, "final", "stage-N" or
// repository for COPY --from) and whether it exists.
func (c *Config) ImageFor(key string) (Image, bool) {
	img, ok := c.images[key]
	return img, ok
}

// PackageFor returns the table for an npm package and whether it exists.
func (c *Config) PackageFor(name string) (Package, bool) {
	p, ok := c.Packages[name]
	return p, ok
}

// SameRepository reports whether the image table describes ref's repository.
func (i Image) SameRepository(ref registry.Reference) bool {
	want, err := registry.ParseReference(i.Image)
	return err == nil && want.Repository() == ref.Repository()
}
