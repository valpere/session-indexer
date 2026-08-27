# Versioning and Release Policy

## Version scheme

Standard [SemVer](https://semver.org/) (`MAJOR.MINOR.PATCH`), with one
project-specific rule layered on top:

- **Any commit that bumps `internal/db.SchemaVersion` requires at least a
  MINOR version bump — never a PATCH.** A schema bump is, in practice, a
  breaking change for existing users (see "Storage" in `CLAUDE.md`: no
  migration framework by design, the documented recovery path is
  delete-and-re-mine). SemVer technically allows anything to change within
  MINOR while the project is pre-1.0 (`0.y.z`), but this rule exists so a
  schema bump is never mistaken for a routine PATCH — check
  `docs/architecture.md`'s Changelog section before assuming a release is
  a safe drop-in upgrade.
- **PATCH** — bug fixes, dependency bumps, CI/docs changes. No schema
  change, no CLI behavior change a script depending on `session-indexer`
  could notice.
- **MINOR** — new features (new subcommands, new flags, new output
  fields), or a schema bump per the rule above.
- **MAJOR** — reserved. See "What 1.0.0 means" below.

## What 1.0.0 means

Not yet declared. At minimum, before cutting `v1.0.0`:

- The `SchemaVersion` bump policy above should have a real answer for
  "can I upgrade without deleting my DB" beyond "check the migration doc
  if one exists" — either a proper migration mechanism, or an explicit,
  permanent decision to keep the current delete-and-re-mine model with
  1.0 signaling something else (API/CLI surface stability, not schema
  stability).
- The CLI surface (subcommands, flags, `--json` output shapes) should be
  considered something other projects/scripts can depend on without
  watching every release's changelog.

Until then, treat every `0.x` release as capable of a schema bump; read
the changelog before upgrading a project you rely on daily.

## Release branching

Trunk-based. All work — features and fixes alike — branches from and
merges back into `main` via PR (see `CLAUDE.md`'s "Before Any Commit"
section and the repo's branch-protection rules). A release is just a
`vX.Y.Z` tag on whatever commit of `main` is current at cut time; there is
no long-lived `develop` branch and no persistent split between "released"
and "next" code.

**Rationale:** this is a solo-maintained, single-binary Go project with no
field reports (yet) of needing to patch an old release while `main` has
already moved past it. A permanent `develop`/`main` split (git-flow-style)
would mean an ongoing merge/sync tax paid on every single change, for a
scenario — hotfixing a stale release — that hasn't happened. That's the
kind of premature-generality this project's own design principles (see
global `CLAUDE.md`, YAGNI) argue against paying for up front.

**If a hotfix for an old release is ever actually needed** (bug found in
`v0.3.0`, but `main` has since moved on to unreleased `v0.4.0`-track
changes that shouldn't ship yet):

```bash
git checkout -b release/v0.3 v0.3.0   # branch from the tag, not from main
# cherry-pick or reimplement the specific fix here
git tag v0.3.1
git push origin release/v0.3 v0.3.1   # triggers release.yml on the new tag
```

This creates the branch retroactively, only when actually needed, scoped
to exactly the one fix. It is not maintained ahead of time and does not
need to track `main`.

## Cutting a release

1. Confirm `main` is green (`gh pr checks` / Actions tab) and everything
   intended for the release has merged.
2. Decide the version number per the scheme above (check whether anything
   merged since the last tag bumped `SchemaVersion`).
3. Update `CHANGELOG.md` with a new `## [X.Y.Z] - YYYY-MM-DD` section.
   `release.yml`'s `generate_release_notes: true` already produces a
   PR-title-based changelog on the GitHub Release page itself — the
   `CHANGELOG.md` entry should be a shorter, thematically-grouped summary
   (Added/Changed/Fixed), not a duplicate of that raw PR list.
4. Update `var version` in `cmd/session-indexer/main.go` to match — it's
   only the fallback used when `git describe --tags --exact-match` can't
   resolve a tag (see `Makefile`'s `LDFLAGS`), but keeping it current
   means `go install` between releases still reports something close to
   accurate.
5. Commit both (CHANGELOG + version bump) directly to `main` via a small
   PR (or as the final commit before tagging — either is fine, this is a
   docs/metadata-only change).
6. Tag and push:
   ```bash
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```
   `.github/workflows/release.yml` triggers on the `v*` tag push, builds
   binaries for `linux/amd64`, `linux/arm64`, `darwin/amd64`,
   `darwin/arm64`, and `windows/amd64`, and creates the GitHub Release
   with those binaries attached plus auto-generated PR-based notes.
7. Verify: `gh release view vX.Y.Z` — check the binaries are attached and
   the release isn't marked a draft/prerelease unintentionally.
