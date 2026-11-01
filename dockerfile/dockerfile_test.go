// SPDX-License-Identifier: MIT

package dockerfile

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseInstructions(t *testing.T) {
	src := "# syntax=docker/dockerfile:1\n" +
		"# escape=\\\n" +
		"\n" +
		"FROM alpine:3.22 AS base\n" +
		"# a comment\n" +
		"RUN apk add \\\n" +
		"    # comment inside continuation\n" +
		"\n" +
		"    curl\n" +
		"RUN <<EOF\n" +
		"FROM not-an-instruction\n" +
		"EOF\n" +
		"COPY <<-EOT /etc/motd\n" +
		"\thello\n" +
		"\tEOT\n" +
		"arg X=1\r\n"

	ins := Parse(src)
	require.Len(t, ins, 5)
	assert.Equal(t, "FROM", ins[0].Cmd)
	assert.Equal(t, 4, ins[0].Line)
	assert.Equal(t, "RUN", ins[1].Cmd)
	assert.Equal(t, "apk add     curl", ins[1].Args)
	assert.Equal(t, "RUN", ins[2].Cmd)
	assert.Equal(t, 10, ins[2].Line)
	assert.Equal(t, "COPY", ins[3].Cmd)
	assert.Equal(t, "ARG", ins[4].Cmd)
	assert.Equal(t, "X=1", ins[4].Args)
	assert.Equal(t, 16, ins[4].Line)
}

func TestBacktickEscape(t *testing.T) {
	src := "# escape=`\nFROM mcr.microsoft.com/windows/servercore:ltsc2022\nRUN dir `\n  C:\\\\\nARG V=1.2.3\n"
	refs, _ := Extract(src)
	require.Len(t, refs, 2)
	assert.Equal(t, "mcr.microsoft.com/windows/servercore:ltsc2022", refs[0].Value)
	assert.Equal(t, "1.2.3", refs[1].Value)
	assert.Equal(t, 5, refs[1].Line)
}

func TestExtractVariants(t *testing.T) {
	src := `FROM --platform=$BUILDPLATFORM golang:1.26.10 AS build
FROM build AS test
FROM scratch
FROM ${BASE_IMAGE}
COPY --from=build /out /out
COPY --chown=1000 --from=0 /a /a
COPY --link --from=ghcr.io/owner/tool:v1.2.3 /tool /tool
ARG A=1.0.0 B="2.0.0" C='3.0.0' D
ARG E=
RUN npm i -g @scope/pkg@1.2.3 left-pad@^1.0.0 && corepack prepare pnpm@latest --activate; echo pnpm@9.0.0
RUN git clone git@github.com:o/r.git && npx --yes cowsay@1.6.0;
RUN ["npm", "install", "x@1.0.0"]
`
	refs, skips := Extract(src)
	var got []string
	for _, r := range refs {
		got = append(got, r.Kind.String()+" "+r.Instr+" "+r.Name+"="+r.Value)
		assert.Equal(t, r.Value, src[r.Start:r.End], "source range of %s", r.Name)
	}
	assert.Equal(t, []string{
		"image FROM golang:1.26.10=golang:1.26.10",
		"image COPY ghcr.io/owner/tool:v1.2.3=ghcr.io/owner/tool:v1.2.3",
		"arg ARG A=1.0.0",
		"arg ARG B=2.0.0",
		"arg ARG C=3.0.0",
		"package RUN @scope/pkg=1.2.3",
		"package RUN pnpm=latest",
		"package RUN cowsay=1.6.0",
	}, got)

	var reasons []string
	for _, s := range skips {
		reasons = append(reasons, s.Text+": "+s.Reason)
	}
	assert.Contains(t, reasons, "build: refers to an earlier build stage")
	assert.Contains(t, reasons, "${BASE_IMAGE}: image uses a build argument")
}

func TestValueSplitAcrossContinuation(t *testing.T) {
	src := "FROM ubuntu:24.04@sha256:\\\nabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd\n"
	refs, skips := Extract(src)
	assert.Empty(t, refs)
	require.Len(t, skips, 1)
	assert.Contains(t, skips[0].Reason, "line continuation")
}

func TestExtractDevcontainer(t *testing.T) {
	data, err := os.ReadFile("../testdata/Dockerfile.devcontainer")
	require.NoError(t, err)
	refs, _ := Extract(string(data))

	type want struct {
		line int
		kind Kind
		name string
	}
	var got []want
	for _, r := range refs {
		name := r.Name
		if r.Kind == KindImage {
			name = r.Value[:min(len(r.Value), 20)]
		}
		got = append(got, want{r.Line, r.Kind, name})
	}
	assert.Equal(t, []want{
		{2, KindImage, "ubuntu:24.04@sha256:"},
		{14, KindImage, "ubuntu:24.04@sha256:"},
		{37, KindImage, "ubuntu:24.04@sha256:"},
		{43, KindArg, "NODE_VERSION"},
		{71, KindImage, "ubuntu:24.04@sha256:"},
		{75, KindArg, "USERNAME"},
		{76, KindArg, "USER_UID"},
		{77, KindArg, "USER_GID"},
		{78, KindArg, "ZSH_AUTOSUGGESTIONS_SHA"},
		{79, KindArg, "NPM_VERSION"},
		{143, KindImage, "ghcr.io/zizmorcore/z"},
		{144, KindImage, "golang:1.26.10@sha25"},
		{147, KindImage, "ghcr.io/astral-sh/uv"},
		{148, KindImage, "ghcr.io/aquasecurity"},
		{149, KindImage, "ghcr.io/hadolint/had"},
		{162, KindPackage, "corepack"},
		{164, KindPackage, "pnpm"},
	}, got)
}

func TestApply(t *testing.T) {
	out, err := Apply(
		"hello world",
		[]Edit{{Start: 6, End: 11, New: "there"}, {Start: 0, End: 5, New: "hi"}},
	)
	require.NoError(t, err)
	assert.Equal(t, "hi there", out)

	_, err = Apply("hello", []Edit{{Start: 0, End: 3, New: "x"}, {Start: 2, End: 4, New: "y"}})
	require.ErrorIs(t, err, ErrOverlap)
}

func TestWords(t *testing.T) {
	ws := Words(`a "b c" 'd e' f\ g  h`)
	var texts []string
	for _, w := range ws {
		texts = append(texts, w.Text)
	}
	assert.Equal(t, []string{"a", `"b c"`, `'d e'`, `f\ g`, "h"}, texts)
}

// Each reference names the config table that lists it.
func TestRefKeys(t *testing.T) {
	src := `FROM ubuntu:24.04
FROM ubuntu:24.04 AS Build
COPY --from=ghcr.io/astral-sh/uv:0.13.6@sha256:` + strings.Repeat("a", 64) + ` /uv /bin/
COPY --from=build /x /x
FROM golang:1.27.3 AS tools
RUN npm install -g corepack@0.36.0 && corepack prepare pnpm@12.16.1+sha512.0a1b --activate
FROM docker.io/library/ubuntu
`
	refs, _ := Extract(src)
	var got []string
	for _, r := range refs {
		got = append(
			got,
			fmt.Sprintf("%d %s %s stage=%q manager=%q", r.Line, r.Instr, r.Key, r.Stage, r.Manager),
		)
	}
	assert.Equal(t, []string{
		`1 FROM stage-0 stage="" manager=""`,
		`2 FROM Build stage="Build" manager=""`,
		`3 COPY ghcr.io/astral-sh/uv stage="" manager=""`,
		`5 FROM tools stage="tools" manager=""`,
		`6 RUN corepack stage="" manager="npm"`,
		`6 RUN pnpm stage="" manager="corepack"`,
		`7 FROM final stage="" manager=""`,
	}, got)
	assert.Equal(t, "12.16.1+sha512.0a1b", refs[5].Value)
	assert.Equal(t, "ghcr.io/owner/tool", Repository("ghcr.io/owner/tool:1.0@sha256:abc"))
	assert.Equal(t, "localhost:5000/tool", Repository("localhost:5000/tool"))
}
