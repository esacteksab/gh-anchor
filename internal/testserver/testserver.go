// SPDX-License-Identifier: MIT

// Package testserver provides in-process fakes of an OCI registry, the npm registry,
// the Node.js release index and the GitHub REST API, so gh-anchor can be tested
// end-to-end without network access.
package testserver

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Digest returns the deterministic digest the fake registry serves for repo:tag.
func Digest(repo, tag string) string {
	sum := sha256.Sum256([]byte(repo + ":" + tag))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Registry is a fake OCI distribution registry.
type Registry struct {
	*httptest.Server
	Repos map[string][]string // repo (e.g. "library/ubuntu") -> tags

	RequireAuth      bool // challenge with Bearer auth and require the issued token
	PageSize         int  // paginate tag lists with Link headers when > 0
	OmitDigestHeader bool // omit Docker-Content-Digest so clients must hash the body
	// Overrides maps "repo:tag" to a digest that differs from Digest(repo, tag).
	Overrides map[string]string
	// FailFirst makes the first N requests answer 429 Too Many Requests.
	FailFirst int
	// RejectCredentials issues a token to clients that send basic credentials, then
	// answers 403 for it, as GHCR does for tokens without read:packages.
	RejectCredentials bool
	// MaxPageSize answers 400 to tag-list requests whose n exceeds it, when > 0.
	MaxPageSize int

	mu       sync.Mutex
	failures int
	Requests []string // "METHOD path" of every request
}

// Tokens issued by the fake registry. They are not credentials.
const (
	fakeToken         = "fake-registry-token"
	credentialedToken = "fake-credentialed-token" //nolint:gosec // test fixture
)

// NewRegistry starts a fake registry serving repos.
func NewRegistry(repos map[string][]string) *Registry {
	r := &Registry{Repos: repos, Overrides: map[string]string{}}
	r.Server = httptest.NewServer(http.HandlerFunc(r.serve))
	return r
}

// Host returns host:port of the registry (127.0.0.1, so clients use plain HTTP).
func (r *Registry) Host() string { return strings.TrimPrefix(r.URL, "http://") }

// DigestFor returns the digest served for repo:tag.
func (r *Registry) DigestFor(repo, tag string) string {
	if d, ok := r.Overrides[repo+":"+tag]; ok {
		return d
	}
	return Digest(repo, tag)
}

func (r *Registry) serve(w http.ResponseWriter, req *http.Request) {
	if r.rateLimited(req) {
		w.Header().Set("Retry-After", "0")
		http.Error(w, "slow down", http.StatusTooManyRequests)
		return
	}
	if req.URL.Path == "/token" {
		r.serveToken(w, req)
		return
	}
	path, ok := strings.CutPrefix(req.URL.Path, "/v2/")
	if !ok {
		http.NotFound(w, req)
		return
	}
	repo, tag, isTagList := splitRegistryPath(path)
	if !r.authorized(w, req, repo) || path == "" {
		return
	}
	tags, ok := r.Repos[repo]
	if !ok {
		http.Error(w, `{"errors":[{"code":"NAME_UNKNOWN"}]}`, http.StatusNotFound)
		return
	}
	if isTagList {
		r.serveTags(w, req, repo, tags)
		return
	}
	r.serveManifest(w, req, repo, tag, tags)
}

// rateLimited records the request and reports whether it should get a 429.
func (r *Registry) rateLimited(req *http.Request) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Requests = append(r.Requests, req.Method+" "+req.URL.Path)
	if r.failures < r.FailFirst {
		r.failures++
		return true
	}
	return false
}

func (r *Registry) serveToken(w http.ResponseWriter, req *http.Request) {
	token := fakeToken
	if _, _, ok := req.BasicAuth(); ok && r.RejectCredentials {
		token = credentialedToken
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
}

// splitRegistryPath splits "<repo>/tags/list" or "<repo>/manifests/<tag>".
func splitRegistryPath(path string) (repo, tag string, isTagList bool) {
	if repo, ok := strings.CutSuffix(path, "/tags/list"); ok {
		return repo, "", true
	}
	if before, after, ok := strings.Cut(path, "/manifests/"); ok {
		return before, after, false
	}
	return "", "", false
}

// authorized enforces the bearer-token flow when RequireAuth is set, writing the
// challenge or denial itself. It returns false if the request was answered.
func (r *Registry) authorized(w http.ResponseWriter, req *http.Request, repo string) bool {
	authz := req.Header.Get("Authorization")
	if authz == "Bearer "+credentialedToken {
		http.Error(w, `{"errors":[{"code":"DENIED"}]}`, http.StatusForbidden)
		return false
	}
	if r.RequireAuth && authz != "Bearer "+fakeToken {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(
			`Bearer realm="%s/token",service="fake",scope="repository:%s:pull"`, r.URL, repo,
		))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if repo == "" {
		w.WriteHeader(http.StatusOK) // GET /v2/
	}
	return true
}

func (r *Registry) serveManifest(
	w http.ResponseWriter,
	req *http.Request,
	repo, tag string,
	tags []string,
) {
	if !contains(tags, tag) {
		http.Error(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`, http.StatusNotFound)
		return
	}
	body := repo + ":" + tag
	w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
	if !r.OmitDigestHeader {
		w.Header().Set("Docker-Content-Digest", r.DigestFor(repo, tag))
	}
	if req.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		return
	}
	_, _ = w.Write([]byte(body)) //nolint:gosec // G705: a fake manifest, not HTML
}

func (r *Registry) serveTags(w http.ResponseWriter, req *http.Request, repo string, tags []string) {
	if n, err := strconv.Atoi(req.URL.Query().Get("n")); err == nil && r.MaxPageSize > 0 &&
		n > r.MaxPageSize {
		http.Error(w, `{"errors":[{"code":"PAGINATION_NUMBER_INVALID"}]}`, http.StatusBadRequest)
		return
	}
	sorted := append([]string(nil), tags...)
	sort.Strings(sorted)
	if last := req.URL.Query().Get("last"); last != "" {
		i := sort.SearchStrings(sorted, last)
		if i < len(sorted) && sorted[i] == last {
			i++
		}
		sorted = sorted[i:]
	}
	if r.PageSize > 0 && len(sorted) > r.PageSize {
		sorted = sorted[:r.PageSize]
		next := fmt.Sprintf(
			"/v2/%s/tags/list?n=%d&last=%s",
			repo,
			r.PageSize,
			url.QueryEscape(sorted[len(sorted)-1]),
		)
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, next))
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": sorted})
}

func contains(list []string, s string) bool {
	return slices.Contains(list, s)
}

// Integrity is the dist.integrity the fake npm registry serves for name@version.
func Integrity(name, version string) string {
	sum := sha512.Sum512([]byte(name + "@" + version))
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

// Hash is Integrity in corepack's "sha512.<hex>" format.
func Hash(name, version string) string {
	sum := sha512.Sum512([]byte(name + "@" + version))
	return "sha512." + hex.EncodeToString(sum[:])
}

// NPMPackage describes a package served by the fake npm registry.
type NPMPackage struct {
	Versions   []string
	Deprecated []string
	DistTags   map[string]string
}

// NewNPM starts a fake npm registry.
func NewNPM(pkgs map[string]NPMPackage) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name := strings.TrimPrefix(req.URL.Path, "/")
		p, ok := pkgs[name]
		if !ok {
			http.Error(w, `{"error":"Not found"}`, http.StatusNotFound)
			return
		}
		versions := map[string]map[string]any{}
		for _, v := range p.Versions {
			versions[v] = map[string]any{
				"name":    name,
				"version": v,
				"dist":    map[string]any{"integrity": Integrity(name, v)},
			}
		}
		for _, v := range p.Deprecated {
			versions[v] = map[string]any{
				"name": name, "version": v, "deprecated": "do not use",
				"dist": map[string]any{"integrity": Integrity(name, v)},
			}
		}
		w.Header().Set("Content-Type", "application/vnd.npm.install-v1+json")
		_ = json.NewEncoder(w).
			Encode(map[string]any{"name": name, "dist-tags": p.DistTags, "versions": versions})
	}))
}

// NodeRelease is an entry of the fake Node.js index.json.
type NodeRelease struct {
	Version string
	LTS     string // codename, or "" for a Current release
}

// NewNode starts a fake Node.js distribution site serving /index.json.
func NewNode(releases []NodeRelease) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/index.json" {
			http.NotFound(w, req)
			return
		}
		out := make([]map[string]any, 0, len(releases))
		for _, r := range releases {
			var lts any = false
			if r.LTS != "" {
				lts = r.LTS
			}
			out = append(out, map[string]any{"version": r.Version, "lts": lts})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
}

// GitHubTag is a tag in a fake GitHub repository.
type GitHubTag struct {
	Name      string
	SHA       string // commit SHA
	Annotated bool
}

// GitHubRelease is a release in a fake GitHub repository.
type GitHubRelease struct {
	Tag        string
	Prerelease bool
	Draft      bool
}

// GitHubRepo is a fake GitHub repository.
type GitHubRepo struct {
	DefaultBranch string
	Branches      map[string]string // branch -> head SHA
	Tags          []GitHubTag       // newest first, as the API returns them
	Releases      []GitHubRelease
}

// Values reported by the fake /rate_limit endpoint (an authenticated client).
const (
	rateLimit     = 5000
	rateRemaining = 4999
	tagsPerPage   = 2
)

// fakeGitHub serves a subset of the GitHub REST API.
type fakeGitHub struct {
	repos   map[string]GitHubRepo
	baseURL string
}

// NewGitHub starts a fake GitHub REST API. Tag listings are paginated two per page
// so clients must follow Link headers.
func NewGitHub(repos map[string]GitHubRepo) *httptest.Server {
	g := &fakeGitHub{repos: repos}
	srv := httptest.NewServer(g)
	g.baseURL = srv.URL
	return srv
}

func notFound(w http.ResponseWriter) {
	http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if req.URL.Path == "/rate_limit" {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resources": map[string]any{
				"core": map[string]any{"limit": rateLimit, "remaining": rateRemaining, "reset": 0},
			},
		})
		return
	}
	owner, rest, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, "/repos/"), "/")
	name, rest, _ := strings.Cut(rest, "/")
	repo, ok := g.repos[owner+"/"+name]
	if !ok {
		notFound(w)
		return
	}
	parts := strings.Split(rest, "/")
	switch {
	case rest == "":
		_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": repo.DefaultBranch})
	case rest == "tags":
		serveGitHubTags(w, req, g.baseURL, repo.Tags)
	case rest == "releases":
		out := []map[string]any{}
		for _, r := range repo.Releases {
			out = append(
				out,
				map[string]any{"tag_name": r.Tag, "prerelease": r.Prerelease, "draft": r.Draft},
			)
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(rest, "git/ref/") && len(parts) >= 4:
		serveGitHubRef(w, repo, parts[2], strings.Join(parts[3:], "/"))
	case strings.HasPrefix(rest, "git/tags/"):
		serveGitHubTagObject(w, repo, strings.TrimPrefix(rest, "git/tags/"))
	case strings.HasPrefix(rest, "git/commits/") && knownCommit(repo, strings.TrimPrefix(rest, "git/commits/")):
		_ = json.NewEncoder(w).
			Encode(map[string]any{"sha": strings.TrimPrefix(rest, "git/commits/")})
	default:
		notFound(w)
	}
}

func serveGitHubTagObject(w http.ResponseWriter, repo GitHubRepo, sha string) {
	for _, t := range repo.Tags {
		if t.Annotated && annotatedSHA(t) == sha {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sha": sha, "tag": t.Name, "object": map[string]any{"type": "commit", "sha": t.SHA},
			})
			return
		}
	}
	notFound(w)
}

func annotatedSHA(t GitHubTag) string {
	sum := sha256.Sum256([]byte("tag-object:" + t.Name))
	return hex.EncodeToString(sum[:])[:40]
}

func knownCommit(repo GitHubRepo, sha string) bool {
	for _, s := range repo.Branches {
		if s == sha {
			return true
		}
	}
	for _, t := range repo.Tags {
		if t.SHA == sha {
			return true
		}
	}
	return false
}

func serveGitHubTags(w http.ResponseWriter, req *http.Request, base string, tags []GitHubTag) {
	const per = tagsPerPage
	page, _ := strconv.Atoi(req.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	start := min((page-1)*per, len(tags))
	end := min(start+per, len(tags))
	if end < len(tags) {
		w.Header().
			Set("Link", fmt.Sprintf(`<%s%s?page=%d&per_page=%d>; rel="next"`, base, req.URL.Path, page+1, per))
	}
	out := []map[string]any{}
	for _, t := range tags[start:end] {
		out = append(out, map[string]any{"name": t.Name, "commit": map[string]any{"sha": t.SHA}})
	}
	_ = json.NewEncoder(w).Encode(out)
}

func serveGitHubRef(w http.ResponseWriter, repo GitHubRepo, kind, name string) {
	switch kind {
	case "heads":
		sha, ok := repo.Branches[name]
		if !ok {
			notFound(w)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ref": "refs/heads/" + name, "object": map[string]any{"type": "commit", "sha": sha},
		})
	case "tags":
		for _, t := range repo.Tags {
			if t.Name != name {
				continue
			}
			obj := map[string]any{"type": "commit", "sha": t.SHA}
			if t.Annotated {
				obj = map[string]any{"type": "tag", "sha": annotatedSHA(t)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ref": "refs/tags/" + name, "object": obj})
			return
		}
		notFound(w)
	default:
		notFound(w)
	}
}
