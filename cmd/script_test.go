// SPDX-License-Identifier: MIT

package cmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/esacteksab/gh-anchor/cmd"
	"github.com/esacteksab/gh-anchor/internal/testserver"
)

// TestMain lets testscript run gh-anchor in-process as the "gh-anchor" command.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"gh-anchor": cmd.Execute,
	})
}

// Commit SHAs served by the fake GitHub API.
const (
	zshMaster = "4444444444444444444444444444444444444444"
	zshV071   = "a3e8a2a5b2c1d3e4f5a6b7c8d9e0f1a2b3c4d5e6"
	zshV080   = "b3e8a2a5b2c1d3e4f5a6b7c8d9e0f1a2b3c4d5e6"
)

// TestScripts runs testdata/script/*.txtar against fake registries started for each
// script, so the tests are hermetic and need no GITHUB_TOKEN or network access.
//
// Files in a script may use these template actions, expanded before the script runs:
//
//	{{registry}}                 host:port of the fake OCI registry
//	{{digest "library/ubuntu" "24.04"}}   digest the fake registry serves for that tag
//	{{hash "pnpm" "12.16.1"}}            sha512 the fake npm registry serves, as "sha512.<hex>"
func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "../testdata/script",
		Setup: func(env *testscript.Env) error {
			reg := testserver.NewRegistry(map[string][]string{
				"library/ubuntu": {
					"24.04",
					"24.10",
					"25.04",
					"25.10",
					"26.04",
					"26.10",
					"latest",
				},
				"library/golang":     {"1.26.10", "1.27.3", "1.27.3-alpine", "1.28rc1"},
				"zizmorcore/zizmor":  {"1.31.1", "1.32.0", "2.0.0"},
				"hadolint/hadolint":  {"v2.15.1", "v2.15.2", "v2.15.2-alpine", "v3.0.0"},
				"aquasecurity/trivy": {"0.76.1", "0.77.0", "canary"},
			})
			reg.RequireAuth = true
			reg.PageSize = 2

			npm := testserver.NewNPM(map[string]testserver.NPMPackage{
				"npm": {
					Versions:   []string{"12.2.1", "12.3.0", "13.0.0"},
					Deprecated: []string{"12.4.0"},
				},
				"corepack": {Versions: []string{"0.36.0", "0.37.0", "1.0.0"}},
				"pnpm": {
					Versions: []string{"12.16.1", "12.17.2", "13.1.0"},
					DistTags: map[string]string{"latest": "13.1.0"},
				},
			})
			node := testserver.NewNode([]testserver.NodeRelease{
				{Version: "v26.1.0"},
				{Version: "v24.12.0", LTS: "Krypton"},
				{Version: "v22.24.0", LTS: "Jod"},
				{Version: "v22.23.4", LTS: "Jod"},
			})
			gh := testserver.NewGitHub(map[string]testserver.GitHubRepo{
				"zsh-users/zsh-autosuggestions": {
					DefaultBranch: "master",
					Branches:      map[string]string{"master": zshMaster},
					Tags: []testserver.GitHubTag{
						{Name: "v0.8.0", SHA: zshV080, Annotated: true},
						{Name: "v0.7.1", SHA: zshV071},
					},
				},
			})
			env.Defer(func() {
				reg.Close()
				npm.Close()
				node.Close()
				gh.Close()
			})

			env.Setenv("REGISTRY", reg.Host())
			env.Setenv("ANCHOR_NPM_REGISTRY", npm.URL)
			env.Setenv("ANCHOR_NODE_DIST_URL", node.URL)
			env.Setenv("GITHUB_API_URL", gh.URL)
			env.Setenv("GITHUB_TOKEN", "test-token")
			env.Setenv("XDG_CACHE_HOME", filepath.Join(env.WorkDir, ".cache"))
			env.Setenv("HOME", env.WorkDir)
			env.Setenv("DOCKER_CONFIG", filepath.Join(env.WorkDir, ".docker"))
			if gocoverdir := os.Getenv("GOCOVERDIR"); gocoverdir != "" {
				env.Setenv("GOCOVERDIR", gocoverdir)
			}

			funcs := template.FuncMap{
				"registry": reg.Host,
				"digest":   testserver.Digest,
				"hash":     testserver.Hash,
			}
			return expandTemplates(env.WorkDir, funcs)
		},
	})
}

// expandTemplates executes every file under dir that contains "{{" as a text/template.
func expandTemplates(dir string, funcs template.FuncMap) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(data, []byte("{{")) {
			return err
		}
		tmpl, err := template.New(filepath.Base(path)).Funcs(funcs).Parse(string(data))
		if err != nil {
			return err
		}
		var out strings.Builder
		if err := tmpl.Execute(&out, nil); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(out.String()), 0o600)
	})
}
