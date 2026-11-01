// SPDX-License-Identifier: MIT

package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// manifestAccept lists the manifest media types we accept, multi-platform indexes first,
// so the digest returned for a multi-arch tag is the index digest (the same digest
// `docker buildx imagetools inspect` shows and Dependabot pins).
var manifestAccept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

const (
	defaultMaxAttempts = 5
	tagsPageSize       = 1000
	maxTagPages        = 100
	maxRetryDelay      = 60 * time.Second
	maxBodyBytes       = 32 << 20
	errorBodyBytes     = 512
	backoffFactor      = 2
	defaultTimeout     = 60 * time.Second
)

// Client talks to OCI registries.
type Client struct {
	HTTP        *http.Client
	Credentials *Credentials
	UserAgent   string
	MaxAttempts int
	// Sleep is used between retries; tests may replace it.
	Sleep func(context.Context, time.Duration)
	// Warn, if set, is told about each retry, so a slow registry isn't silent.
	Warn func(format string, args ...any)

	mu        sync.Mutex
	tokens    map[string]string // registry|scope -> bearer token
	anonymous map[string]bool   // registry|scope -> credentials were rejected; stay anonymous
}

// credentialFor returns the credential to use for key, unless it was rejected earlier.
func (c *Client) credentialFor(ref Reference, key string) (Credential, bool) {
	c.mu.Lock()
	anon := c.anonymous[key]
	c.mu.Unlock()
	if anon {
		return Credential{}, false
	}
	return c.Credentials.Lookup(ref.Registry)
}

// dropCredentials switches key to anonymous access. It returns false if there were no
// credentials to drop, so callers don't retry forever.
func (c *Client) dropCredentials(ref Reference, key string) bool {
	if _, ok := c.credentialFor(ref, key); !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.anonymous == nil {
		c.anonymous = map[string]bool{}
	}
	c.anonymous[key] = true
	delete(c.tokens, key)
	return true
}

// NewClient returns a Client using creds (which may be nil for anonymous access).
func NewClient(creds *Credentials, userAgent string) *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: defaultTimeout},
		Credentials: creds,
		UserAgent:   userAgent,
	}
}

// Tags lists every tag of ref's repository, following pagination.
func (c *Client) Tags(ctx context.Context, ref Reference) ([]string, error) {
	base := fmt.Sprintf("%s://%s/v2/%s/tags/list", ref.scheme(), ref.apiHost(), ref.Repo)
	next := fmt.Sprintf("%s?n=%d", base, tagsPageSize)
	var tags []string
	for page := 0; next != "" && page < maxTagPages; page++ {
		resp, err := c.do(ctx, ref, http.MethodGet, next, "")
		var se *StatusError
		if page == 0 && errors.As(err, &se) && se.StatusCode == http.StatusBadRequest {
			// Some registries reject large page sizes; let the registry choose.
			next = base
			resp, err = c.do(ctx, ref, http.MethodGet, next, "")
		}
		if err != nil {
			return nil, err
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&body)
		link := resp.Header.Get("Link")
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decoding tag list for %s: %w", ref.Repository(), err)
		}
		tags = append(tags, body.Tags...)
		next, err = nextLink(next, link)
		if err != nil {
			return nil, err
		}
	}
	return tags, nil
}

// Digest returns the manifest digest of ref's repository at tag.
func (c *Client) Digest(ctx context.Context, ref Reference, tag string) (string, error) {
	u := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", ref.scheme(), ref.apiHost(), ref.Repo, tag)
	// HEAD does not count against Docker Hub's pull rate limit.
	resp, err := c.do(ctx, ref, http.MethodHead, u, manifestAccept)
	if IsNotFound(err) {
		return "", fmt.Errorf("tag %q not found in %s: %w", tag, ref.Repository(), err)
	}
	var se *StatusError
	switch {
	case err == nil:
		_ = resp.Body.Close()
		if d := resp.Header.Get("Docker-Content-Digest"); d != "" {
			return d, nil
		}
	case !errors.As(err, &se):
		// A network error: a GET won't fare better.
		return "", err
	}
	// Some registries omit the digest header on HEAD, or refuse HEAD; hash the
	// manifest instead.
	resp, err = c.do(ctx, ref, http.MethodGet, u, manifestAccept)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if d := resp.Header.Get("Docker-Content-Digest"); d != "" {
		return d, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return "", fmt.Errorf("reading manifest for %s:%s: %w", ref.Repository(), tag, err)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// StatusError is returned for unexpected HTTP responses.
type StatusError struct {
	Method, URL string
	StatusCode  int
	Body        string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("%s %s: %d %s", e.Method, e.URL, e.StatusCode, http.StatusText(e.StatusCode))
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// IsNotFound reports whether err is a 404 from the registry.
func IsNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}

// do performs a registry request, handling authentication challenges and retrying
// rate-limited (429) and server (5xx) responses with backoff.
func (c *Client) do(
	ctx context.Context,
	ref Reference,
	method, u, accept string,
) (*http.Response, error) {
	scope := "repository:" + ref.Repo + ":pull"
	key := ref.Registry + "|" + scope
	authenticated := false
	delay := time.Second
	maxAttempts := c.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}

	for attempt := 1; ; attempt++ {
		canRetry := attempt < maxAttempts && ctx.Err() == nil
		resp, err := c.send(ctx, ref, key, method, u, accept)
		switch {
		case err != nil:
			// A network error (refused, timed out) is retried once: it rarely clears
			// up within a run, and each attempt can take the whole client timeout.
			if !canRetry || attempt > 1 {
				return nil, err
			}
			c.warn(ref, err.Error(), delay, attempt, maxAttempts)
			c.sleep(ctx, delay)
		case isSuccess(resp.StatusCode):
			return resp, nil
		case resp.StatusCode == http.StatusUnauthorized && !authenticated:
			challenge := resp.Header.Get("WWW-Authenticate")
			drain(resp)
			if err := c.authenticate(ctx, ref, key, scope, challenge); err != nil {
				return nil, err
			}
			authenticated = true
			continue
		case isDenied(resp.StatusCode) && c.dropCredentials(ref, key):
			// The registry accepted our credentials but they don't grant access to this
			// repository (e.g. a GITHUB_TOKEN without read:packages). Public images
			// are still readable anonymously, so retry without credentials.
			drain(resp)
			authenticated = false
			continue
		case isRetryable(resp.StatusCode) && canRetry:
			wait := retryAfter(resp.Header.Get("Retry-After"), delay)
			drain(resp)
			c.warn(ref, resp.Status, wait, attempt, maxAttempts)
			c.sleep(ctx, wait)
		default:
			return nil, newStatusError(method, u, resp)
		}
		delay = min(delay*backoffFactor, maxRetryDelay)
	}
}

// send builds and sends one request with the cached credentials for ref.
func (c *Client) send(
	ctx context.Context,
	ref Reference,
	key, method, u, accept string,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	c.authorize(req, ref, key)
	return c.HTTP.Do(req)
}

func isSuccess(code int) bool { return code >= http.StatusOK && code < http.StatusMultipleChoices }

func isDenied(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

func isRetryable(code int) bool {
	return code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}

// newStatusError consumes resp and describes it as an error.
func newStatusError(method, u string, resp *http.Response) *StatusError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyBytes))
	_ = resp.Body.Close()
	return &StatusError{
		Method: method, URL: u, StatusCode: resp.StatusCode,
		Body: strings.TrimSpace(string(body)),
	}
}

func (c *Client) authorize(req *http.Request, ref Reference, key string) {
	c.mu.Lock()
	token := c.tokens[key]
	c.mu.Unlock()
	switch {
	case token == basicMarker:
		if cred, ok := c.credentialFor(ref, key); ok {
			req.SetBasicAuth(cred.Username, cred.Password)
		}
	case token != "":
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// basicMarker in the token cache means "send basic credentials".
const basicMarker = "\x00basic"

// authenticate answers a WWW-Authenticate challenge and caches the result.
func (c *Client) authenticate(
	ctx context.Context,
	ref Reference,
	key, scope, challenge string,
) error {
	scheme, params := parseChallenge(challenge)
	switch strings.ToLower(scheme) {
	case "basic":
		if _, ok := c.credentialFor(ref, key); !ok {
			return fmt.Errorf(
				"registry %s requires credentials (see README: Authentication)",
				ref.Registry,
			)
		}
		c.setToken(key, basicMarker)
		return nil
	case "bearer":
	default:
		return fmt.Errorf(
			"registry %s sent an unsupported auth challenge %q",
			ref.Registry,
			challenge,
		)
	}

	realm := params["realm"]
	if realm == "" {
		return fmt.Errorf("registry %s sent a bearer challenge without a realm", ref.Registry)
	}
	if s := params["scope"]; s != "" {
		scope = s
	}
	q := url.Values{}
	if svc := params["service"]; svc != "" {
		q.Set("service", svc)
	}
	q.Set("scope", scope)
	tokenURL := realm + "?" + q.Encode()

	cred, haveCred := c.credentialFor(ref, key)
	token, err := c.fetchToken(ctx, tokenURL, cred, haveCred)
	if err != nil && haveCred {
		// Credentials that lack access (e.g. a GITHUB_TOKEN without read:packages)
		// should not break pulls of public images: retry anonymously.
		token, err = c.fetchToken(ctx, tokenURL, Credential{}, false)
	}
	if err != nil {
		return fmt.Errorf("authenticating to %s: %w", ref.Registry, err)
	}
	c.setToken(key, token)
	return nil
}

func (c *Client) fetchToken(
	ctx context.Context,
	tokenURL string,
	cred Credential,
	useCred bool,
) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", err
	}
	if useCred {
		req.SetBasicAuth(cred.Username, cred.Password)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", &StatusError{Method: http.MethodGet, URL: tokenURL, StatusCode: resp.StatusCode}
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&body); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", errors.New("token response contained no token")
}

func (c *Client) setToken(key, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokens == nil {
		c.tokens = map[string]string{}
	}
	c.tokens[key] = token
}

// warn reports a retry through Warn.
func (c *Client) warn(ref Reference, reason string, wait time.Duration, attempt, maxAttempts int) {
	if c.Warn != nil {
		c.Warn(
			"⏳  %s: %s; retrying in %s (attempt %d of %d)",
			ref.Registry,
			reason,
			wait,
			attempt+1,
			maxAttempts,
		)
	}
}

func (c *Client) sleep(ctx context.Context, d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
	_ = resp.Body.Close()
}

// retryAfter parses a Retry-After header in seconds, falling back to def.
func retryAfter(h string, def time.Duration) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, maxRetryDelay)
	}
	return def
}

// parseChallenge parses `Bearer realm="...",service="...",scope="..."`.
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if strings.HasPrefix(v, `"`) {
			end := strings.IndexByte(v[1:], '"')
			if end < 0 {
				params[k] = v[1:]
				break
			}
			params[k] = v[1 : end+1]
			rest = v[end+2:]
		} else {
			val, after, _ := strings.Cut(v, ",")
			params[k] = strings.TrimSpace(val)
			rest = after
		}
	}
	return scheme, params
}

// nextLink resolves an RFC 5988 `Link: <...>; rel="next"` header against current.
func nextLink(current, header string) (string, error) {
	if header == "" {
		return "", nil
	}
	for part := range strings.SplitSeq(header, ",") {
		target, params, ok := strings.Cut(part, ";")
		if !ok || !strings.Contains(strings.ReplaceAll(params, " ", ""), `rel="next"`) {
			continue
		}
		target = strings.Trim(strings.TrimSpace(target), "<>")
		base, err := url.Parse(current)
		if err != nil {
			return "", err
		}
		next, err := base.Parse(target)
		if err != nil {
			return "", fmt.Errorf("invalid Link header %q: %w", header, err)
		}
		return next.String(), nil
	}
	return "", nil
}
