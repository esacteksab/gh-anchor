# gh-anchor

`gh-anchor` is a GitHub CLI extension that pins and updates the versions and digests in your Dockerfiles, including the places Dependabot doesn't look.

## Features

- **Explicit:** only what `.gh-anchor.toml` lists is updated. [`gh anchor init`](#gh-anchor-init) writes a table for everything it finds; without a config nothing is changed
- **Records** each image's tag and digest and each package's version and hash in `.gh-anchor.toml`. The Dockerfile wins: a stale record is overwritten with the Dockerfile's value, with a warning
- Pins images in `FROM` **and** `COPY --from=` to their `sha256` digest, and gives an image pinned by digest only (`trivy@sha256:…`) the tag that has that digest, so it can be followed
- **Targets:** `target = "24"` moves to the newest 24.x and stays there (with `lts = true`, the next LTS line), so you choose when to change lines
- **Asks first:** in a terminal, a run asks whether to update or only audit; without a config it runs `init` for you
- Refreshes digests with `-u/--update`, keeping `FROM` base image tags and moving `COPY --from` images to the newest release in their major version
- Updates `ARG` defaults (`ARG NODE_VERSION=22.23.4`) from npm, Node.js, Docker registries and GitHub (tags, releases or commits)
- Updates inline `package@version` arguments to `npm`, `npx`, `pnpm`, `yarn` and `corepack` in `RUN`, pinning packages passed to corepack with their hash (`pnpm@12.16.1+sha512.<hex>`)
- `-u/--update` stays within the current major version. **Bumping** crosses majors and moves base images (only to LTS releases when you ask), per item: `bump = true` in the config, `-b=golang,pnpm` for one run, an interactive picker with `-b -i`, or everything with a bare `-b`
- Ends every run with a summary: how many references were checked, updated, already up to date, skipped (and why) or failed
- `gh anchor init` generates a `.gh-anchor.toml` from your Dockerfile, checks it against the registries, looks up package hashes, and lets you pick what to manage
- Preserves file formatting: only the version or digest itself is rewritten
- Implements local HTTP caching to reduce API calls

## Why?

Dependabot's `docker` ecosystem reads `FROM` lines. It does not update images referenced in `COPY --from=`, versions passed through `ARG`, or tools installed with a pinned version inside `RUN`. A typical devcontainer Dockerfile has all three:

```dockerfile
ARG NODE_VERSION=22.23.4
ARG ZSH_AUTOSUGGESTIONS_SHA=85919cd1ffa7d2d5412f6d3fe437ebdbeeec4fc5

COPY --from=ghcr.io/astral-sh/uv:0.13.6@sha256:af22a52f... /uv /uvx /bin/
COPY --link --from=golang:1.26.10@sha256:670bb5aa... /usr/local/go /usr/local/go

RUN npm install --global --prefix /usr/local corepack@0.36.0 \
    && corepack prepare pnpm@12.16.1 --activate
```

`gh-anchor` keeps every one of those current.

## Installation

### Prerequisites

- [GitHub CLI](https://cli.github.com/) (`gh`) installed

### Install as a GitHub CLI extension

```bash
gh ext install esacteksab/gh-anchor
```

## Usage

> [!NOTE]
> `gh-anchor` is designed to be run in the root directory of your repository. With no arguments it processes `./Dockerfile` and reads `./.gh-anchor.toml`; pass paths to process other Dockerfiles. Without a `.gh-anchor.toml`, nothing is updated: run `gh anchor init` first.

### Quick start

```bash
gh anchor init     # list everything in ./Dockerfile in .gh-anchor.toml
gh anchor          # pin: add digests, tags and package hashes, record them in .gh-anchor.toml
gh anchor -u       # refresh digests and update within the current major versions
gh anchor -u --audit  # show what -u would change and what isn't listed; write nothing
```

In a terminal, `gh anchor` (with any flags) first asks **Update** or **Audit**; `--yes` skips the question and updates, `--audit` audits without asking. Without a `.gh-anchor.toml`, it runs [`gh anchor init`](#gh-anchor-init) first and then asks whether to update right away (choose No to review the config, e.g. to set `bump` or `target`). Without a terminal there are no questions: it updates, or, with no config, changes nothing and says so.

### Commands

| Command                          | What it does                                                                                                |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| `gh anchor [Dockerfile...]`      | Pin, update (`-u`) or bump (`-b`) the references in the Dockerfiles (default `./Dockerfile`).               |
| `gh anchor init [Dockerfile...]` | Generate a config file from the Dockerfiles. Alias: `gh anchor i`. See [`gh anchor init`](#gh-anchor-init). |
| `gh anchor clear --force`        | Delete the local HTTP cache. See [Managing Local Cache](#managing-local-cache).                             |

For example:

- `gh anchor`: Pin floating references (see below) and change nothing else.
- `gh anchor -u`: Refresh digests and update to the newest release **within the current major version**. `FROM` base images keep their tag.
- `gh anchor -b`: Like `--update`, but **bump everything**: also move to new major versions and new `FROM` tags.
- `gh anchor -b=golang,pnpm`: Update everything, and bump only those two tables (plus any with `bump = true`). See [Bumping](#bumping).
- `gh anchor -b -i`: Pick what to bump in an interactive form.
- `gh anchor -u build/Dockerfile Dockerfile.ci`: Process specific Dockerfiles.
- `gh anchor -u -c ci/anchor.toml`: Use a config file other than `.gh-anchor.toml`.
- `gh anchor -u -v`: Log every reference that was checked, what was looked up and the outcome (see [Debugging](#debugging)).

### Flags

| Flag                  | Type    | Commands                      | Default                 | Description                                                                                                                                                                                                                                           |
| --------------------- | ------- | ----------------------------- | ----------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `-u`, `--update`      | bool    | `gh anchor`                   | `false`                 | Refresh digests; move ARGs, `COPY --from` images and packages to the newest release in their major version.                                                                                                                                           |
| `-b`, `--bump`        | strings | `gh anchor`                   | none; bare `-b` = `all` | Like `--update`, and also bump: cross major versions and move `FROM` tags (LTS only with `lts = true`). A bare `-b` bumps everything; `-b=key,...` (or repeated `--bump=key`) bumps only those tables. Attach keys with `=`. See [Bumping](#bumping). |
| `-i`, `--interactive` | bool    | `gh anchor`                   | `false`                 | With `-b`: pick the tables to bump in a form. Needs a terminal.                                                                                                                                                                                       |
| `--audit`             | bool    | `gh anchor`                   | `false`                 | Don't write anything: report what the run would change and record, out-of-date records, items the config doesn't list and tables that match nothing. See [Auditing](#auditing).                                                                       |
| `-y`, `--yes`         | bool    | `gh anchor`, `gh anchor init` | `false`                 | `gh anchor`: update without asking "Update or Audit?". `gh anchor init`: write the config without prompting (an existing file is backed up first).                                                                                                    |
| `-c`, `--config`      | string  | all                           | `.gh-anchor.toml`       | Config file to read (`gh anchor`) or write (`gh anchor init`). Must be inside the current directory.                                                                                                                                                  |
| `-v`, `--verbose`     | bool    | all                           | `false`                 | Debug logging: one line per reference with what was looked up and the outcome. Same as `ANCHOR_DEBUG=true`.                                                                                                                                           |
| `--debug`             | bool    | all                           | `false`                 | Same as `--verbose`.                                                                                                                                                                                                                                  |
| `--stdout`            | bool    | `gh anchor init`              | `false`                 | Print the config instead of writing it. Can't be combined with `--yes`.                                                                                                                                                                               |
| `--offline`           | bool    | `gh anchor init`              | `false`                 | Don't look anything up; decide from the Dockerfile alone.                                                                                                                                                                                             |
| `-f`, `--force`       | bool    | `gh anchor clear`             | `false`                 | Required to delete the cache.                                                                                                                                                                                                                         |
| `--version`           | bool    | `gh anchor`                   | `false`                 | Print the version.                                                                                                                                                                                                                                    |
| `-h`, `--help`        | bool    | all                           | `false`                 | Show help.                                                                                                                                                                                                                                            |

### What each mode changes

| Reference                                             | `gh anchor` (pin)                                      | `-u/--update`                                | bumped (`bump = true`, `-b`)                      |
| ----------------------------------------------------- | ------------------------------------------------------ | -------------------------------------------- | ------------------------------------------------- |
| `FROM ubuntu:24.04`                                   | adds `@sha256:` digest                                 | keeps `24.04`, adds digest                   | newest tag (newest **LTS** tag with `lts = true`) |
| `FROM ubuntu:24.04@sha256:...`                        | unchanged                                              | keeps `24.04`, fresh digest                  | newest tag (or LTS tag), fresh digest             |
| `COPY --from=ghcr.io/o/tool@sha256:...` (digest only) | adds the tag with that digest: `tool:1.2.3@sha256:...` | adds the tag, then newest `1.x.y` + digest   | adds the tag, then newest + digest                |
| `COPY --from=ghcr.io/o/tool:1.2.3`                    | adds digest                                            | newest `1.x.y` + digest                      | newest + digest                                   |
| `ARG NODE_VERSION=22` (partial)                       | newest `22.x.y`                                        | newest `22.x.y`                              | newest                                            |
| `ARG NPM_VERSION=12.2.1`                              | unchanged                                              | newest `12.x.y`                              | newest                                            |
| `ARG ..._SHA=<sha>` (`github-commits`)                | unchanged (branch/tag names → SHA)                     | branch head, or newest tag in the same major | branch head, newest tag                           |
| `RUN corepack prepare pnpm@latest`                    | `pnpm@<latest>+sha512.<hex>`                           | `pnpm@<latest>+sha512.<hex>`                 | `pnpm@<latest>+sha512.<hex>`                      |
| `RUN corepack prepare pnpm@12.16.1`                   | `pnpm@12.16.1+sha512.<hex>`                            | newest `12.x.y` + hash, capped at `latest`   | newest + hash, capped at `latest`                 |
| `RUN npm install -g corepack@0.36.0`                  | unchanged (hash recorded)                              | newest `0.x.y`, capped at `latest`           | newest, capped at `latest`                        |

Only references listed in `.gh-anchor.toml` are changed; the others are counted as skipped (`not in .gh-anchor.toml`). In every mode, the tag, digest, version and hash a reference ends up with are recorded in its table.

Packages passed to `corepack` get the package's `sha512` appended (`name@version+sha512.<hex>`, the format of `packageManager` in `package.json`), which corepack verifies when it downloads the package. npm, pnpm and yarn ignore a `+…` suffix, so for them the hash is only recorded in the config, unless the Dockerfile already carries one. The hash comes from the npm registry's `dist.integrity` (sha512, base64) converted to hex.

`FROM` lines keep their tag under `--update` because the base image release (Ubuntu 24.04, Debian bookworm) is a deliberate choice; only bumping changes it. `fixed-tag = true` keeps a tag even when bumped.

### Targets

`target` picks the version line an item moves to and stays on. It is a version prefix: `target = 24` (or `"24"`) is the newest 24.x.y; `target = "1.26"` keeps golang on 1.26.x even when 1.27 is out; `target = "26.04"` moves an Ubuntu image to 26.04.

- It applies with `-u` and `-b` (plain `gh anchor` only pins), whether or not the table has `bump = true`, and bumping never goes past it. It overrides the major-version rule (22 → 24 under `-u`) and `FROM`'s keep-the-tag rule. A target below the current version moves down, since it's an explicit choice; the debug line says so.
- **With `lts = true`**, only LTS releases count, and a line that has none moves to the **next LTS line at or above** it: with Node.js LTS lines 22, 24 and 26, `target = 25` gives the newest 26.x, `target = 23` the newest 24.x. Without `lts`, the line must have a release.
- Write a line with a dot as a string (`target = "1.26"`): as a TOML number, `1.20` would read as `1.2`, so numbers with a fraction are rejected. Whole numbers are fine unquoted.
- `fixed-tag = true` and a target can't be combined. A `github-commits` ARG needs `track = "tags"` to have a target.

```toml
[args.NODE_VERSION]
  datasource = "node-version"
  lts        = true
  target     = 24       # Node 24 LTS, even though 26 is out
```

### Bumping

Bumping lets an item move to a new major version, and a `FROM` image to a new tag (the newest LTS tag with `lts = true`). It is chosen per table, so one tool can follow new majors while the rest stays put:

| How                          | Bumps                                                           | Everything else          |
| ---------------------------- | --------------------------------------------------------------- | ------------------------ |
| `bump = true` in a table     | that table, on every `gh anchor -u` (and `-b=…`) run            | updated within its major |
| `gh anchor -b=golang,pnpm`   | the named tables, plus those with `bump = true`, for this run   | updated within its major |
| `gh anchor -b -i`            | the tables you pick (pre-selected: `bump = true`), for this run | updated within its major |
| `gh anchor -b` (or `-b=all`) | every table                                                     | (nothing else)           |

- **Names:** use a table's key (`golang`, `pnpm`, `NODE_VERSION`, `final`, `ghcr.io/zizmorcore/zizmor`) or its full name when the key is in more than one section (`images.node`, `packages.node`). Full names also work with or without quotes: `-b='images."ghcr.io/o/r"'` or `-b=images.ghcr.io/o/r`. An unknown or ambiguous name, or a table with `skip = true`, is an error, and nothing is changed.
- **Attach names with `=`:** `-b=golang`, `--bump=golang`. Because a bare `-b` means "all", `gh anchor -b golang` reads `golang` as a Dockerfile; gh-anchor then reminds you to write `-b=golang`.
- **The picker** (`-b -i`) lists every table that isn't skipped. Move with `↑`/`↓`, toggle with `x` or `space`, `ctrl+a` for all, `enter` to run, `q` or `esc` to quit without changing anything. Its choice applies to this run only; set `bump = true` in the config to make it stick. `-b=key -i` pre-selects those keys too.
- Plain `gh anchor` (pin mode) never bumps, even with `bump = true`.
- A table with a [`target`](#targets) stays in its line even when bumped.

Each run says what it bumps, e.g. `Bumping images.golang, packages.pnpm; updating the rest within its major version`. With `-v`, each bumped reference's line starts with `bumping (bump = true)` or `bumping (-b)`.

### How the newest version is chosen

- **A target wins.** With `target = "24"` in the table, `-u` and `-b` move to the newest release in the 24 line, even across majors or to an older line, and stay in it. See [Targets](#targets).

- **Same shape.** A candidate must have the same prefix, number of components and suffix as the current value. `1.26.10` can move to `1.27.3` but never to `1.27`, `1.27.3-alpine` or `1.28rc1`; `v2.15.1` stays `v`-prefixed.

- **Major version** is the first number. `-u` keeps it; `-b` doesn't. For `0.x` versions this means `0.13.6 → 0.14.0` is allowed with `-u`.

- **Pre-releases are skipped.** Anything with a suffix (`-rc.1`, `-beta`) only matches a current value with the same suffix.

- **npm `latest` is respected.** Versions newer than the package's `latest` dist-tag (for example pnpm's `next-12` channel) are never selected.

- **Tags that aren't versions** (`latest`, `noble`, `canary`) only get their digest refreshed.

- **LTS images.** With `lts = true`, an image only moves to LTS tags. Ubuntu is built in (an even year and `.04`: `24.04 → 26.04`, skipping `24.10`, `25.04`, `25.10` and `26.10`); for other images, set `lts-pattern`.

- **Digests** are the multi-platform index digest, the same one `docker buildx imagetools inspect` reports.

- **Package hashes** are the `sha512` npm publishes for the version (`dist.integrity`). A hash in the Dockerfile that doesn't match is reported as an error for that reference.

## `gh anchor init`

`gh anchor init` scans the Dockerfile (or the Dockerfiles you pass) and generates `.gh-anchor.toml` (or the file given with `-c`). Because `gh-anchor` only updates what the config lists, init writes a table for every image, package and version ARG it finds, with every setting spelled out, defaults included.

```console
$ gh anchor init
Scanned Dockerfile: 3 ARG(s) with a datasource, 9 image(s), 2 package(s), 0 ARG(s) without a detected datasource
Checking 14 item(s) against npm, Node.js, GitHub and the image registries…
```

### What it detects

- **ARGs** whose name contains `VERSION` or ends in `_SHA` or `_COMMIT`. Others, such as `USERNAME` or `USER_UID`, aren't versions and are ignored. The datasource comes from:

  1. **The name**, for well-known ARGs:

     | ARG                              | Datasource                       |
     | -------------------------------- | -------------------------------- |
     | `NODE_VERSION`, `NODEJS_VERSION` | `node-version` with `lts = true` |
     | `NPM_VERSION`                    | `npm`, package `npm`             |
     | `PNPM_VERSION`                   | `npm`, package `pnpm`            |
     | `YARN_VERSION`                   | `npm`, package `yarn`            |
     | `COREPACK_VERSION`               | `npm`, package `corepack`        |
     | `GO_VERSION`, `GOLANG_VERSION`   | `docker`, image `golang`         |
     | `PYTHON_VERSION`                 | `docker`, image `python`         |

  1. **Where it is used**, otherwise:

     | Usage                                                                                                         | Datasource                              |
     | ------------------------------------------------------------------------------------------------------------- | --------------------------------------- |
     | `FROM image:${ARG}` or `COPY --from=image:${ARG}`                                                             | `docker`, that image                    |
     | `https://nodejs.org/dist/v${ARG}/...`                                                                         | `node-version` with `lts = true`        |
     | `https://registry.npmjs.org/<pkg>/-/<pkg>-${ARG}.tgz` or `.../<pkg>/${ARG}`                                   | `npm`, that package                     |
     | `<pkg>@${ARG}` after `npm`, `npx`, `pnpm`, `yarn`, `corepack` or `bun`                                        | `npm`, that package                     |
     | `https://github.com/<o>/<r>/releases/download/v${ARG}/...`                                                    | `github-releases`                       |
     | `https://github.com/<o>/<r>/archive/(refs/tags/)v${ARG}.tar.gz`, `raw.githubusercontent.com/<o>/<r>/v${ARG}/` | `github-tags`                           |
     | `https://go.dev/dl/go${ARG}...`                                                                               | `docker`, image `golang`                |
     | `https://www.python.org/ftp/python/${ARG}/...`                                                                | `docker`, image `python`                |
     | A `git` command using `${ARG}` after `git clone`/`git remote add` of a GitHub repository                      | `github-commits` (SHA) or `github-tags` |

  ARGs that match neither are listed as comments at the end of the file, so you can add them by hand. ARGs whose default refers to another variable (`ARG B_VERSION=${A_VERSION}`) are ignored.

- **Images** in `FROM` and `COPY --from`, recording the `image`, `tag` and `digest` the Dockerfile uses (see [`[images.<key>]`](#imageskey-settings) for the keys). Init adds safe defaults: `lts = true` for Ubuntu (built-in LTS rule) and for Node.js images (with `lts-pattern` for even major versions), so bumping only moves between LTS releases. Every table gets `bump = false`; set it to `true` for the items that should follow new major versions.

- **Packages** passed as `name@version` to `npm`, `npx`, `pnpm`, `yarn` or `corepack` in `RUN`, recording the `version` and `hash`.

If two references would share a table with different values (for example two Dockerfiles whose unnamed final stages use different images, or `pnpm@12.16.1` and `pnpm@12.17.2`), init reports each one and writes nothing. Name the stages (`FROM image AS name`) or use one version.

### Lookups

Unless you pass `--offline`, every suggestion is checked against npm, Node.js, GitHub and the image registries:

- **Commit SHAs:** a SHA that is a tagged release gets `track = "tags"`; otherwise `track = "branch"`, with a note if it isn't the head of the default branch.
- **GitHub versions:** a `github-releases` suggestion whose value is a tag but not a release becomes `github-tags`.
- **Package hashes:** the `sha512` of each package version is looked up on npm and recorded as `hash`. A hash already in the Dockerfile is kept, with a note if npm publishes a different one. A dist-tag such as `latest` is noted; the first run pins a version.
- **Not found:** an npm package, image or GitHub repository that doesn't exist rejects the ARG suggestion. It is written commented out and deselected. A package that doesn't exist gets `skip = true`; an image whose tag isn't found is only noted.
- **Lookup errors:** if a lookup fails for another reason (network, rate limit), the offline suggestion is kept with a `Not verified` note.

Results are written as comments above each entry, for example `# Verified: Node.js 22.23.4 is an LTS release`.

### The form

In a terminal, init shows an interactive prompt-based form:

1. **Which items should gh-anchor manage?** Everything detected is listed and pre-selected. Move with `↑`/`↓` (or `k`/`j`), toggle with `x` or `space`, select all/none with `ctrl+a`, and press `enter` to continue. Deselected items are still written, with `skip = true`, so the config stays a complete record of the Dockerfile.
1. **Create new file `.gh-anchor.toml`?** or **Overwrite existing config file `.gh-anchor.toml`?** Choose Yes (`y`) or No (`n`), or switch with `←`/`→` and press `enter`. Creating defaults to Yes and overwriting to No. Choosing No prints the config instead. When overwriting, the existing file is first copied to `.gh-anchor.toml-YYYYMMDDHHMM`.

Press `q`, `esc` or `ctrl+c` to quit without writing anything. Set `ACCESSIBLE=1` to run the form in accessible mode (plain-text prompts that work with screen readers).

### Scripts and CI

Without a terminal, init prints the config and writes nothing unless you pass `--yes`:

```bash
gh anchor init --yes            # write .gh-anchor.toml (backing up an existing one)
gh anchor init --stdout         # print it
gh anchor init --offline --yes  # no network; decide from the Dockerfile alone
```

## Configuration

`.gh-anchor.toml` (in the current directory, or the file given with `-c`) is the explicit list of what `gh-anchor` updates, and a record of what the Dockerfile uses. Generate it with [`gh anchor init`](#gh-anchor-init) or write it by hand.

- **Only what is listed is updated.** An image, package or `ARG` without a table is left alone and counted as skipped (`not in .gh-anchor.toml`). Without a config file, nothing is updated.
- **Image and package tables are records.** `tag`, `digest`, `version` and `hash` hold what the Dockerfile uses. After each run, `gh-anchor` writes the new values back: an existing value is replaced in place, keeping comments and alignment, and a missing field is added after the table's last key. Empty values are filled in. Edited tables must be written as `[table]` headers (not dotted keys or inline tables).
- **The Dockerfile wins.** Before anything is looked up, every recorded value (`image`, `tag`, `digest`, `version`, `hash`) is compared with the Dockerfile. A value that differs (for example, after a hand edit, or a config copied from another project) is reported as a warning and overwritten with the Dockerfile's value; the run then goes on as usual and records the final values. If a lookup fails, the record still ends up matching the Dockerfile.
- **One table records one thing.** If a table is used by two references with different values (two Dockerfiles whose unnamed final stages differ, say), overwriting can't fix it: `gh-anchor` reports each conflict, changes neither file and exits with status 1. Name the stages (`FROM image AS name`) or use one version.
- **Policy settings** are yours to edit: `bump`, [`target`](#targets), `lts`, `fixed-tag` and `skip`. `gh-anchor` never changes them.

```toml
# FROM ubuntu:24.04@sha256:fe81560990fa… AS python (Dockerfile:2)
[images.python]
  image     = "ubuntu"
  tag       = "24.04"
  digest    = "sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852"
  fixed-tag = false
  lts       = true   # bumping moves 24.04 -> 26.04, never to an interim release
  target    = ""
  bump      = false
  skip      = false

# FROM ubuntu:24.04@sha256:fe81560990fa… (Dockerfile:71)
[images.final]
  image     = "ubuntu"
  tag       = "24.04"
  digest    = "sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852"
  fixed-tag = false
  lts       = true
  target    = ""
  bump      = false
  skip      = false

# COPY --from=ghcr.io/zizmorcore/zizmor:1.31.1@sha256:42d409dbb851… (Dockerfile:143)
[images."ghcr.io/zizmorcore/zizmor"]
  image     = "ghcr.io/zizmorcore/zizmor"
  tag       = "1.31.1"
  digest    = "sha256:42d409dbb851d4c5d35a058cf042b828bea1e3f2884378c526eb0974780e18d9"
  fixed-tag = false
  lts       = false
  target    = ""
  bump      = true   # follow new major versions on every -u run
  skip      = false

# RUN corepack … pnpm@12.16.1 (Dockerfile:164)
[packages.pnpm]
  version = "12.16.1"
  hash    = "sha512.091d1dee7927575661b25dab3c2ae31cafc1c54d6b71ed6a6600cf0c1cb10434a447eae14015f1b23e2e6bedeed79df94a932e2164d2832280dce3fb32f656d1"
  target  = ""
  bump    = false
  skip    = false

[args.NODE_VERSION]
  datasource = "node-version"
  lts        = true   # only LTS release lines when bumping
  target     = 24     # newest Node 24, not 26: see Targets
  bump       = false
  skip       = false

[args.ZSH_AUTOSUGGESTIONS_SHA]
  datasource = "github-commits"
  repo       = "zsh-users/zsh-autosuggestions"
  track      = "branch"   # follow the default branch (or set branch = "<name>")
  bump       = false
  skip       = false

[args.GO_VERSION]
  datasource = "docker"
  image      = "golang"
  target     = ""
  bump       = false
  skip       = false

[args.HADOLINT_VERSION]
  datasource = "github-releases"
  repo       = "hadolint/hadolint"
  target     = ""
  bump       = false
  skip       = false
```

Unknown keys are errors, so a typo like `pakage` is reported instead of ignored.

### `[images.<key>]` settings

The key says which reference the table describes:

| Reference                            | Key                                                       | Example                                |
| ------------------------------------ | --------------------------------------------------------- | -------------------------------------- |
| `FROM image AS name`                 | the stage name, as written                                | `[images.python]`                      |
| `FROM image` (unnamed), last stage   | `final`                                                   | `[images.final]`                       |
| `FROM image` (unnamed), other stages | `stage-N`, Docker's stage index (counting from 0)         | `[images.stage-0]`                     |
| `COPY --from=image`                  | the repository as written, without tag or digest (quoted) | `[images."ghcr.io/zizmorcore/zizmor"]` |

Several `COPY --from` lines with the same repository share one table, and so must use the same tag and digest. `COPY --from=<stage name>` refers to a build stage and is not an image.

| Setting       | Type              | Required                                | Default | Description                                                                                                                                                                                                      |
| ------------- | ----------------- | --------------------------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `image`       | string            | yes (unless `skip = true`)              |         | The image repository, without tag or digest, e.g. `"ubuntu"` or `"ghcr.io/owner/tool"`. Must match the Dockerfile; `ubuntu`, `docker.io/library/ubuntu` and `index.docker.io/library/ubuntu` are the same image. |
| `tag`         | string            | no                                      | `""`    | Recorded tag. An image without a tag is recorded as `"latest"`. Empty means not recorded yet, or an image pinned by digest only (the first run adds and records its tag).                                        |
| `digest`      | string            | no                                      | `""`    | Recorded digest, `"sha256:<64 hex>"`. Empty means not recorded yet.                                                                                                                                              |
| `fixed-tag`   | bool              | no                                      | `false` | Never change the tag, even when bumped; only refresh the digest.                                                                                                                                                 |
| `lts`         | bool              | no                                      | `false` | Only move to LTS tags. Built in for Ubuntu (an even year and `.04`); other images need `lts-pattern`.                                                                                                            |
| `lts-pattern` | string            | with `lts = true` for non-Ubuntu images |         | Regular expression matching LTS tags. TOML literal strings (`'...'`) avoid doubling backslashes.                                                                                                                 |
| `target`      | string or integer | no                                      | `""`    | Version line to move to and stay on, e.g. `"26.04"` or `"1.26"`. With `lts = true`, the next LTS line at or above. Not with `fixed-tag`. See [Targets](#targets).                                                |
| `bump`        | bool              | no                                      | `false` | Bump on every `-u` run: allow a new major version, and a new tag for a `FROM` image. See [Bumping](#bumping).                                                                                                    |
| `skip`        | bool              | no                                      | `false` | Leave this image alone. Recorded values aren't checked.                                                                                                                                                          |

### `[packages.<name>]` settings

`<name>` is the npm package name, quoted if it contains `@` or `/`: `[packages."@scope/pkg"]`. Every `name@version` of that package in `RUN` shares the table, and so must use the same version.

| Setting   | Type              | Required | Default | Description                                                                                          |
| --------- | ----------------- | -------- | ------- | ---------------------------------------------------------------------------------------------------- |
| `version` | string            | no       | `""`    | Recorded version (without the `+sha512…` suffix). Empty means not recorded yet.                      |
| `hash`    | string            | no       | `""`    | Recorded hash of that version as `"sha512.<hex>"` (corepack's format). Empty means not recorded yet. |
| `target`  | string or integer | no       | `""`    | Version line to move to and stay on, e.g. `10`. See [Targets](#targets).                             |
| `bump`    | bool              | no       | `false` | Bump on every `-u` run: allow a new major version. See [Bumping](#bumping).                          |
| `skip`    | bool              | no       | `false` | Leave `name@version` in `RUN` alone. Recorded values aren't checked.                                 |

### `[args.<NAME>]` settings

`<NAME>` is the ARG name exactly as written in the Dockerfile. ARG values are not recorded in the config: the Dockerfile is the record.

| Setting      | Type              | Required                                                | Default                         | Description                                                                                                                                                         |
| ------------ | ----------------- | ------------------------------------------------------- | ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `datasource` | string            | yes (unless `skip = true`)                              |                                 | Where versions come from: `node-version`, `npm`, `docker`, `github-tags`, `github-releases`, `github-commits`.                                                      |
| `package`    | string            | with `npm`                                              |                                 | npm package name, e.g. `"pnpm"` or `"@scope/pkg"`.                                                                                                                  |
| `image`      | string            | with `docker`                                           |                                 | Image repository, e.g. `"golang"` or `"ghcr.io/owner/repo"`.                                                                                                        |
| `repo`       | string            | with `github-tags`, `github-releases`, `github-commits` |                                 | GitHub repository as `"owner/name"`.                                                                                                                                |
| `track`      | string            | no                                                      | `"branch"`                      | `github-commits` only: follow a branch head (`"branch"`) or tagged releases (`"tags"`).                                                                             |
| `branch`     | string            | no                                                      | the repository's default branch | `github-commits` with `track = "branch"`: the branch to follow.                                                                                                     |
| `lts`        | bool              | no                                                      | `false`                         | `node-version` only: when bumping, only consider LTS release lines.                                                                                                 |
| `target`     | string or integer | no                                                      | `""`                            | Version line to move to and stay on, e.g. `24`. With `lts = true`, the next LTS line at or above. `github-commits` needs `track = "tags"`. See [Targets](#targets). |
| `bump`       | bool              | no                                                      | `false`                         | Bump on every `-u` run: allow a new major version. See [Bumping](#bumping).                                                                                         |
| `skip`       | bool              | no                                                      | `false`                         | Leave this ARG alone.                                                                                                                                               |

### `args` datasources

| `datasource`      | Options                                                               | ARG value is                           |
| ----------------- | --------------------------------------------------------------------- | -------------------------------------- |
| `node-version`    | `lts = true` to only consider LTS releases                            | a Node.js version, e.g. `22.23.4`      |
| `npm`             | `package` (required)                                                  | an npm version                         |
| `docker`          | `image` (required), e.g. `"golang"` or `"ghcr.io/o/r"`                | an image tag                           |
| `github-tags`     | `repo = "owner/name"` (required)                                      | a Git tag, with or without its `v`     |
| `github-releases` | `repo` (required); drafts and pre-releases skipped                    | a release tag, with or without its `v` |
| `github-commits`  | `repo` (required), `track = "branch"` (default) or `"tags"`, `branch` | a 40-character commit SHA              |

Every entry also accepts `target`, `bump` and `skip`.

For `github-tags` and `github-releases`, `ARG HADOLINT_VERSION=2.15.1` matches the tag `v2.15.1` and is updated without adding the `v`, since such ARGs are usually used as `v${HADOLINT_VERSION}`.

> [!TIP]
> With `track = "tags"`, `-u` finds the tag that points at the current commit and moves to the newest tag in the same major version. If the commit isn't tagged, bump it (`bump = true` or `-b=NAME`) or use `track = "branch"`.

## Authentication

> [!TIP]
> For higher REST API rate limits, [create a GitHub token](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens) and set `GITHUB_TOKEN` (or `GH_TOKEN`) as an environment variable:

```bash
export GITHUB_TOKEN=your_token_here
gh anchor -u
```

Registry credentials are looked up in this order:

1. **Docker Hub:** `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN`
1. **ghcr.io:** `GITHUB_TOKEN` / `GH_TOKEN` (needs `read:packages` for private images; public images fall back to anonymous access)
1. **Any registry:** `auths` entries in `$DOCKER_CONFIG/config.json` (default `~/.docker/config.json`)

Credential helpers (`credsStore`, `credHelpers`) are not executed. Digests are resolved with `HEAD` requests, which don't count toward Docker Hub's pull rate limit.

### Upgrade `gh anchor`

```bash
gh ext upgrade anchor
```

## Managing Local Cache

The extension caches npm, Node.js and GitHub API responses to reduce API calls. You can clear this cache using the `clear` command with the required `-f` or `--force` flag:

> [!NOTE]
> The `-f/--force` flag is required as a safeguard to prevent accidental cache deletion.

```bash
gh anchor clear -f
```

This will remove the application's cache directory located at:

- **Linux/BSD**: `$XDG_CACHE_HOME/gh-anchor` (typically `~/.cache/gh-anchor`)
- **macOS**: `~/Library/Caches/gh-anchor`
- **Windows**: `%LocalAppData%\gh-anchor` (typically `C:\Users\<username>\AppData\Local\gh-anchor`)

## Examples

### Updating a devcontainer Dockerfile

```console
$ gh anchor init --yes
Scanned Dockerfile: 3 ARG(s) with a datasource, 9 image(s), 2 package(s), 0 ARG(s) without a detected datasource
Checking 14 item(s) against npm, Node.js, GitHub and the image registries…
Config file .gh-anchor.toml created
$ gh anchor -u
Processing Dockerfile: Dockerfile
🔧  Authenticated GitHub API access in effect.
Changes in Dockerfile:
  line 2: FROM ubuntu:24.04@sha256:fe81560990fa… → ubuntu:24.04@sha256:6fbfd46ee8bd…
  line 14: FROM ubuntu:24.04@sha256:fe81560990fa… → ubuntu:24.04@sha256:6fbfd46ee8bd…
  line 37: FROM ubuntu:24.04@sha256:fe81560990fa… → ubuntu:24.04@sha256:6fbfd46ee8bd…
  line 71: FROM ubuntu:24.04@sha256:fe81560990fa… → ubuntu:24.04@sha256:6fbfd46ee8bd…
  line 144: COPY golang:1.26.10@sha256:670bb5aa5349… → golang:1.27.3@sha256:2920bb124f1a…
  line 164: RUN pnpm 12.16.1 → 12.16.1+sha512.091d1dee7927…
✅  Updated 6 reference(s) in Dockerfile
Summary for Dockerfile: 17 checked, 6 updated, 8 up to date, 3 skipped (not in .gh-anchor.toml: 3), 0 failed
✅  Recorded 8 value(s) in .gh-anchor.toml
Finished processing. Total references updated across all files: 6
```

The three skipped references are `USERNAME`, `USER_UID` and `USER_GID`, which aren't versions. The recorded values are the four new `FROM` digests, golang's tag and digest, and the hashes of `pnpm` and `corepack`. (Digests above are illustrative.)

### Pinning

Before:

```dockerfile
FROM ubuntu:24.04 AS base
COPY --from=ghcr.io/hadolint/hadolint:v2.15.1 /bin/hadolint /bin/
RUN corepack prepare pnpm@latest --activate
```

After `gh anchor` (with a table for each in `.gh-anchor.toml`):

```dockerfile
FROM ubuntu:24.04@sha256:<digest> AS base
COPY --from=ghcr.io/hadolint/hadolint:v2.15.1@sha256:<digest> /bin/hadolint /bin/
RUN corepack prepare pnpm@<latest version>+sha512.<hex> --activate
```

and `.gh-anchor.toml` records the tags, digests, version and hash.

### When the Dockerfile and the config disagree

The Dockerfile wins: stale records are overwritten, and the run goes on.

```console
$ gh anchor -u --yes
Processing Dockerfile: Dockerfile
WARN ⚠️  line 144: COPY: [images.golang] recorded tag = "1.27.3", but the Dockerfile has "1.26.3": recording the Dockerfile's
WARN ⚠️  line 164: RUN pnpm: [packages.pnpm] recorded version = "12.16.1", but the Dockerfile has "10.33.0": recording the Dockerfile's
Changes in Dockerfile:
  line 144: COPY golang:1.26.3@sha256:313faae491b4… → golang:1.26.4@sha256:…
...
✅  Recorded 5 value(s) in .gh-anchor.toml
```

### Auditing

```console
$ gh anchor -u --audit
Processing Dockerfile: Dockerfile
Would change in Dockerfile:
  line 143: COPY ghcr.io/zizmorcore/zizmor:1.24.1@sha256:128ebbe369a9… → ghcr.io/zizmorcore/zizmor:1.24.2@sha256:…
Not in .gh-anchor.toml (run gh anchor init to add them):
  line 150: COPY ghcr.io/astral-sh/ty:0.0.1: no [images."ghcr.io/astral-sh/ty"] table
Would record in .gh-anchor.toml:
  [images."ghcr.io/zizmorcore/zizmor"] tag = "1.24.2"
Summary for Dockerfile: 17 checked, 1 updated, 13 up to date, 3 skipped (not in .gh-anchor.toml: 3), 0 failed
Unused: [images.old] matches nothing in [Dockerfile]
Audit: 1 change(s) pending, 2 value(s) to record (0 out of date), 0 conflict(s), 1 not listed, 1 unused table(s), 0 failed. Nothing was written.
INFO ℹ️  Run gh anchor again and choose Update (or pass --yes) to apply the changes.
```

An audit runs the same lookups as the update it stands in for (`-u --audit`, `-b=golang --audit`, ...) and reports, without writing:

- **changes** the run would make, and the values it would **record**;
- **out-of-date records** (which an update would overwrite with the Dockerfile's values) and **conflicts** (a table used with two values);
- **not listed:** images, packages and version ARGs (`*VERSION*`, `*_SHA`, `*_COMMIT`) without a table;
- **unused** tables that match nothing in the Dockerfiles.

What it finds is information, not an error: it ends with `✅  Everything is current and listed.` or `INFO ℹ️  Run gh anchor again and choose Update (or pass --yes) to apply the changes.`, and exits with status 0 either way. Only a lookup or file that failed makes it exit with status 1. In a terminal, choosing **Audit** at the "Update or Audit?" prompt does the same.

(Values above are illustrative.)

## Limitations

- Image references built from variables (`FROM ${BASE_IMAGE}`, `COPY --from=${IMAGE}`) are skipped; describe the variable as an `ARG` with the `docker` datasource instead.
- `ARG` values that reference other variables (`ARG B=${A}`) are skipped.
- Inline packages are only detected after `npm`, `npx`, `pnpm`, `pnpx`, `yarn`, `corepack`, `bun` and `bunx`, in shell-form `RUN` (not exec-form `["..."]` or heredoc bodies).
- `RUN --mount=from=<image>` is not updated.
- Dockerfile instructions can't carry trailing comments, so there is no `# v1.2.3` note next to a commit SHA the way [`gh-actlock`](https://github.com/esacteksab/gh-actlock) adds one.
- `gh anchor init` only considers ARGs named `*VERSION*`, `*_SHA` or `*_COMMIT`, and only recognizes the names and usages listed under [What it detects](#what-it-detects).
- Recorded values are written back only to tables written as `[section.key]` headers, not to dotted keys or inline tables.
- Package hashes are only pinned in the Dockerfile for corepack (and where one is already present); npm, pnpm and yarn ignore the `+sha512…` suffix.
- Several Dockerfiles that share one config also share its tables, so their unnamed final stages must use the same image (or be named).
- An image pinned by digest only gets a tag only if one of its 50 newest version tags has that digest; otherwise it is skipped (`no version tag has this digest`).
- Tags with a changing suffix (`1.27.3-2026-10-30`, `1.27.3-alpine3.22`) are not updated across suffix changes.

## Troubleshooting

Every run ends with a summary per Dockerfile. If something you expected to change didn't, the summary says whether it was already up to date, skipped (and why: for example `not in .gh-anchor.toml` or `skip = true`) or failed:

```text
Summary for Dockerfile: 17 checked, 0 updated, 14 up to date, 3 skipped (not in .gh-anchor.toml: 3), 0 failed
```

| Message                                                                             | Meaning and fix                                                                                                  |
| ----------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| `No .gh-anchor.toml found, so nothing was updated`                                  | Run `gh anchor init` to list what to update.                                                                     |
| `skipped (not in .gh-anchor.toml)`                                                  | Add a table for it (or re-run `gh anchor init`).                                                                 |
| `WARN … recorded tag = "…", but the Dockerfile has "…": recording the Dockerfile's` | The record was stale (hand edit, or a config from elsewhere). Nothing to do: it is overwritten.                  |
| `[images.final] is used with … here but … on line N of …`                           | Two references share one table with different values; nothing was changed. Name the stages (`AS name`).          |
| `the hash of pnpm@x in the Dockerfile doesn't match the one npm publishes`          | The `+sha512.…` in the Dockerfile is wrong. Remove it and run `gh anchor` to pin the right one.                  |
| `Failed to process golang` … `attach it to the flag: -b=golang`                     | Keys go after `=`: `-b=golang`. A bare `-b` means "bump everything".                                             |
| `-b: "node" matches images.node and packages.node`                                  | Use the full name: `-b=images.node`.                                                                             |
| `skipped (no version tag has this digest)`                                          | The pinned digest isn't one of the image's 50 newest version tags. Add the tag yourself: `image:1.2.3@sha256:…`. |
| `target 25: no LTS release in the 25 line or above`                                 | No LTS line at or above the target exists yet; lower the target or remove `lts = true`.                          |
| `unknown key(s): images."…".bump` (or `.target`)                                    | The installed gh-anchor predates the setting: reinstall from the latest release.                                 |

### Debugging

Pass `-v`, `--verbose` or `--debug`, or set the environment variable `ANCHOR_DEBUG` to `true` or `1` (`TRUE`, `True`, `t` and `T` also work):

```bash
gh anchor -u -v
gh anchor -u --debug
ANCHOR_DEBUG=true gh anchor -u
```

Debug output shows the config that was loaded, then one line per reference in file order: the current value, what was looked up, and the outcome. Lines that aren't references (build-stage names, `FROM scratch`, `ARG`s without a default) are listed as `ignored`:

```text
DEBU Loaded .gh-anchor.toml: args [NODE_VERSION, NPM_VERSION, ZSH_AUTOSUGGESTIONS_SHA], images [final, gh, ghcr.io/aquasecurity/trivy, ghcr.io/astral-sh/uv, ghcr.io/hadolint/hadolint, ghcr.io/zizmorcore/zizmor, golang, node, python], packages [corepack, pnpm]
DEBU found 17 references (9 images, 6 ARGs, 2 packages) and 3 other candidates
DEBU line 2: FROM ubuntu:24.04@sha256:fe81560990fa…: base image: keeping tag "24.04" (bump = true or -b changes it); digest unchanged → up to date
DEBU line 43: ARG NODE_VERSION 22.23.4: 1043 Node.js releases; 512 LTS; newest in major 22: 22.23.4 (current) → up to date
DEBU line 75: ARG USERNAME devcontainer: skipped (not in .gh-anchor.toml)
DEBU line 78: ARG ZSH_AUTOSUGGESTIONS_SHA 85919cd1ffa7…: zsh-users/zsh-autosuggestions master is at 85919cd1ffa7 → up to date
DEBU line 79: ARG NPM_VERSION 12.2.1: npm npm: 812 versions, latest 12.2.1; newest in major 12: 12.2.1 (current) → up to date
DEBU line 142: COPY gh: ignored (refers to an earlier build stage)
DEBU line 143: COPY ghcr.io/zizmorcore/zizmor:1.31.1@sha256:42d409dbb851…: 61 tags in ghcr.io/zizmorcore/zizmor; newest in major 1: 1.31.1 (current); digest unchanged → up to date
DEBU line 144: COPY golang:1.26.10@sha256:670bb5aa5349…: 2412 tags in docker.io/library/golang; newest in major 1: 1.27.3; digest of 1.27.3 changed to sha256:2920bb124f1a… → updated to golang:1.27.3@sha256:2920bb124f1a…
DEBU line 164: RUN pnpm 12.16.1+sha512.091d1dee7927…: npm pnpm: 1489 versions, latest 12.16.1; ignoring 1 newer than latest; newest in major 12: 12.16.1 (current) → up to date
```

(Counts and digests above are illustrative.) If you believe you found a bug, please open an issue with this output. Thank you!

### Environment variables

| Variable               | Default                      | Purpose                                                                                              |
| ---------------------- | ---------------------------- | ---------------------------------------------------------------------------------------------------- |
| `ANCHOR_DEBUG`         | unset                        | `true`/`1` enables debug logging, like `-v`/`--verbose`/`--debug`                                    |
| `ACCESSIBLE`           | unset                        | `true`/`1` runs the `gh anchor init` form in accessible mode (plain-text prompts for screen readers) |
| `GITHUB_TOKEN`         | unset                        | GitHub API and ghcr.io authentication (`GH_TOKEN` also works)                                        |
| `DOCKERHUB_USERNAME`   | unset                        | Docker Hub username (with `DOCKERHUB_TOKEN`)                                                         |
| `DOCKERHUB_TOKEN`      | unset                        | Docker Hub access token                                                                              |
| `DOCKER_CONFIG`        | `~/.docker`                  | Directory holding `config.json` with registry `auths`                                                |
| `ANCHOR_NPM_REGISTRY`  | `https://registry.npmjs.org` | npm registry (e.g. a mirror)                                                                         |
| `ANCHOR_NODE_DIST_URL` | `https://nodejs.org/dist`    | Node.js release index (`index.json`)                                                                 |
| `GITHUB_API_URL`       | `https://api.github.com/`    | GitHub API (GitHub Enterprise Server)                                                                |

## License

MIT Licensed

## Contributing

Contributions welcome! Please feel free to submit a pull request.

### Similar Tools

- [Renovate](https://docs.renovatebot.com/modules/manager/dockerfile/) can update `ARG` values with [regex managers](https://docs.renovatebot.com/modules/manager/regex/)
- [Dependabot](https://docs.github.com/en/code-security/dependabot) for `FROM` lines

## Development

```bash
make test      # unit + testscript tests (hermetic: fake registries, no network or token needed)
make audit     # tidy, fmt, vet, staticcheck, govulncheck, golangci-lint
make build     # goreleaser snapshot build
make install   # build and install as a local gh extension
```

`internal/testserver` fakes the registries; `scaffold` holds the `init` detection, lookups and rendering. The end-to-end tests in `testdata/script/*.txtar` run `gh-anchor` against in-process fakes of an OCI registry, the npm registry, the Node.js release index and the GitHub API (`internal/testserver`).
