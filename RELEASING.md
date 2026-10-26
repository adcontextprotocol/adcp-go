# Releasing

Releases are cut by [release-please](https://github.com/googleapis/release-please)
(`.github/workflows/release.yml`) from `release-please-config.json` and
`.release-please-manifest.json`. Each sub-module has its own release PR
(`chore(main): release <component> <version>`); merging it creates the
`<component>/vX.Y.Z` tag and GitHub release. A release PR only opens when
there are releasable commits (`feat`, `fix`, `perf`, ...) under the module's
directory.

## `main` is the AdCP 3.3 pre-release line for `adcp/v3`

Until AdCP 3.3 is ratified, `main` carries the experimental 3.3 work (agent
identity: Web Bot Auth, trust.json, grants), so the `adcp/v3` module releases
**pre-releases only** from `main`. This matches the TypeScript SDK, whose `main`
publishes `16.0.0-beta.N`. See adcontextprotocol/adcp#8118.

The `adcp/v3` package config sets:

```json
"versioning": "prerelease",
"prerelease-type": "beta.0",
"prerelease": true
```

What release-please does with that:

| Current version | Commits since last release | Next version |
| --- | --- | --- |
| `3.2.1` (stable) | `feat` | `3.3.0-beta.0` |
| `3.3.0-beta.N` | `feat` or `fix` | `3.3.0-beta.N+1` |

- Tags look like `adcp/v3.3.0-beta.0`, a valid Go module version:
  `go get github.com/adcontextprotocol/adcp-go/adcp/v3@v3.3.0-beta.0`.
  `@latest` still resolves to the newest stable `v3.2.x`, because Go skips
  pre-releases when a stable release exists.
- The GitHub release is marked as a pre-release.
- Do not land breaking-change commits (`feat!:` or a `BREAKING CHANGE:` footer)
  under `adcp/v3/`. release-please would propose `4.0.0-beta.*`, which is not a
  valid version for the `adcp/v3` module path.

Every other module (`urlcanon`, `registry`, `registry/redisstore`,
`registry/glidestore`, `tmproto`, `tmpclient`, `targeting`, `uid2client`)
releases stable versions from `main` as usual.

## Stable `adcp/v3` 3.2.x fixes before 3.3 ratifies

`main` cannot ship a stable `adcp/v3` release during this period. If a 3.2.x fix
is needed, create a release branch then, not ahead of time:

1. Land the fix on `main` first through a normal PR.
2. Branch from the last stable tag and cherry-pick the fix:

   ```sh
   git fetch --tags origin
   git switch -c release/v3.2.x adcp/v3.2.1   # or the latest adcp/v3.2.x tag
   git cherry-pick -x <sha>
   git push -u origin release/v3.2.x
   ```

   Open later fixes as PRs into `release/v3.2.x`.
3. Tag the release by hand. The release workflow only runs on `main`, and
   pointing release-please at the branch would also try to release the other
   modules. Add an `adcp/v3/CHANGELOG.md` entry on the branch, then:

   ```sh
   git tag adcp/v3.2.2
   git push origin adcp/v3.2.2
   gh release create adcp/v3.2.2 --verify-tag --title "adcp: v3.2.2" --notes "..."
   ```

4. Forward-port: the fix is already on `main` (step 1). Copy the 3.2.2
   changelog entry into `main`'s `adcp/v3/CHANGELOG.md` if you want the history
   in one place. Leave `.release-please-manifest.json` on `main` alone: it
   tracks the 3.3 pre-release line, and `3.3.0-beta.N` already sorts above
   `3.2.2`.

## Leaving pre-release when AdCP 3.3 ratifies

1. In `release-please-config.json`, set `"prerelease": false` for `adcp/v3`
   (keep `"versioning": "prerelease"`). From `3.3.0-beta.N`, the next `feat` or
   `fix` release PR then proposes `3.3.0`, and the GitHub release is not marked
   as a pre-release. Do not just delete the three keys: the default versioning
   strategy keeps the pre-release suffix and would propose `3.4.0-beta.N`
   (`feat`) or `3.3.1-beta.N` (`fix`).
2. If no releasable `adcp/v3/` commit has landed since the last beta, nothing
   opens a release PR. Either land one, or add `"release-as": "3.3.0"` instead
   and remove it in a follow-up PR once `adcp/v3.3.0` is tagged (otherwise
   every later release PR stays pinned to 3.3.0).
3. Merge the regenerated release PR to tag `adcp/v3.3.0`.
4. Optional cleanup after `adcp/v3.3.0` exists: remove `versioning`,
   `prerelease-type` and `prerelease`. From a stable version they behave the
   same as the default strategy.
