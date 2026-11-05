# go-template

Template for new Go repos under `github.com/icco`. Config only — no Go source to unpick.

## After creating a repo from this template

1. **`go mod init go.icco.me/<repo>`.** Every workflow reads `go-version-file: go.mod`, so CI fails until the module exists.
2. **Replace `PROJECT_NAME` in `.goreleaser.yaml`** (4 occurrences: `project_name`, `release.github.name`, and twice in the release footer). Left unsubstituted, the first release publishes into a repo that does not exist.
3. **Check Actions are enabled** — `gh api repos/icco/<repo>/actions/permissions`. They are deliberately disabled on this template so its own workflows never fire; children should inherit the org default of enabled, but confirm once.
4. **Replace this `README.md`** with the new repo's own. GitHub copies it across verbatim, so a fresh repo otherwise ships a README describing itself as a template.
5. **Update the `LICENSE` copyright year** if the year has rolled over.

## Choices baked in

- **Library by default.** `.goreleaser.yaml` sets `builds: [{skip: true}]` — the release exists for the changelog and the tag `go get` resolves against. For a binary, drop `skip` and add a real `builds:` block; see `icco/etu` for the `homebrew_casks:` pattern that publishes into `icco/homebrew-tap` (it needs a `GH_PAT` secret, since the default `GITHUB_TOKEN` cannot push to another repo).
- **No docker dependabot ecosystem.** Add it back for a service that ships a `Dockerfile`.
- **Coverage floor is 80%**, enforced in `test.yml` via `COVERAGE_THRESHOLD`. Lower it deliberately if a repo cannot meet it, rather than deleting the check.
- **Packages start at `v1.0.0`**, not `v0.1.0`.

## Why the config looks like this

Each of these is a fix for something that silently misbehaved:

- **`.golangci.yml` is committed rather than passed as `-E` flags in the workflow.** Inline flags mean a local run and CI enforce different sets, so findings only appear after a push.
- **`.yamllint` disables `document-start` and `line-length` and sets `truthy: check-keys: false`.** The defaults flag `on:` in every workflow.
- **`release.yml`'s `major_pattern`/`minor_pattern` are wrapped in `/ /`.** A bare `feat:` is a literal substring match and misses scoped commits like `feat(vertex):`, silently downgrading a minor bump to a patch.
- **`yaml-json.yml` stages with `file_pattern: '. :!.github/workflows/*'`.** A literal `*.json` pathspec makes `git add` exit 128 in a repo with no JSON. The exclusion stays because the default `GITHUB_TOKEN` cannot edit files under `.github/workflows`.
- **`pr-title.yml` validates the PR *title*, not commit messages** — `type: subject`, lowercase, no trailing period. Fix a bad title with `gh pr edit --title`, never a force push.

## Note on `std-error-handling`

`.golangci.yml` enables the `std-error-handling` exclusion preset, which suppresses `errcheck` on things like `defer resp.Body.Close()`. It also hides genuine unchecked errors. If you want those surfaced, remove the `exclusions.presets` block and re-run the linter.
