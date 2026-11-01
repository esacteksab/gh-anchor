// SPDX-License-Identifier: MIT

package registry

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Credential is a username/password (or token) pair for a registry.
type Credential struct {
	Username string
	Password string
}

// Credentials resolves registry credentials. Lookup order:
//
//  1. Docker Hub: DOCKERHUB_USERNAME + DOCKERHUB_TOKEN
//  2. ghcr.io: GITHUB_TOKEN (or GH_TOKEN), sent with the username "gh-anchor"
//  3. `auths` entries in $DOCKER_CONFIG/config.json (default ~/.docker/config.json)
//
// Credential helpers (credsStore / credHelpers) are not executed.
type Credentials struct {
	static map[string]Credential
}

// LoadCredentials builds Credentials from the environment and the Docker config file.
func LoadCredentials(getenv func(string) string) *Credentials {
	c := &Credentials{static: map[string]Credential{}}

	if u, t := getenv("DOCKERHUB_USERNAME"), getenv("DOCKERHUB_TOKEN"); u != "" && t != "" {
		c.static[DockerHub] = Credential{Username: u, Password: t}
	}
	token := getenv("GITHUB_TOKEN")
	if token == "" {
		token = getenv("GH_TOKEN")
	}
	if token != "" {
		c.static["ghcr.io"] = Credential{Username: "gh-anchor", Password: token}
	}

	dir := getenv("DOCKER_CONFIG")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".docker")
		}
	}
	if dir != "" {
		c.loadDockerConfig(filepath.Join(dir, "config.json"))
	}
	return c
}

func (c *Credentials) loadDockerConfig(path string) {
	data, err := os.ReadFile(path) //nolint:gosec // well-known config location
	if err != nil {
		return
	}
	var cfg struct {
		Auths map[string]struct {
			Auth     string `json:"auth"`
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auths"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return
	}
	for host, a := range cfg.Auths {
		cred := Credential{Username: a.Username, Password: a.Password}
		if a.Auth != "" {
			if raw, err := base64.StdEncoding.DecodeString(a.Auth); err == nil {
				if u, p, ok := strings.Cut(string(raw), ":"); ok {
					cred = Credential{Username: u, Password: p}
				}
			}
		}
		if cred.Username == "" || cred.Password == "" {
			continue
		}
		key := normalizeHost(host)
		if _, exists := c.static[key]; !exists {
			c.static[key] = cred
		}
	}
}

// normalizeHost maps docker config keys such as "https://index.docker.io/v1/" to a registry name.
func normalizeHost(h string) string {
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	h, _, _ = strings.Cut(h, "/")
	h = strings.ToLower(h)
	switch h {
	case "index.docker.io", dockerHubAPI:
		return DockerHub
	}
	return h
}

// Lookup returns the credential for registry, if any. A nil receiver has none.
func (c *Credentials) Lookup(registry string) (Credential, bool) {
	if c == nil {
		return Credential{}, false
	}
	cred, ok := c.static[strings.ToLower(registry)]
	return cred, ok
}
