# Releasing

## Semver guidelines

| Bump | When |
|------|------|
| **Major** `v2.0.0` | Breaking changes: renamed or removed commands, MCP tools or flags, changed config or profile layout |
| **Minor** `v0.x.0` | New commands, MCP tools, flags or features, fully backward-compatible |
| **Patch** `v0.1.x` | Bug fixes, dependency updates, documentation, packaging |

## Pre-release checklist

- [ ] `make test` and `make lint` pass
- [ ] Integration suite passes against the test site, see [integration/README.md](integration/README.md)
- [ ] GoReleaser dry run succeeds: `goreleaser release --snapshot --clean --skip=publish,sign`
- [ ] README reflects new commands, tools or flags
- [ ] `CHANGELOG.md`: move the entries under "Unreleased" into a new `## [x.y.z] - YYYY-MM-DD` section and add its link at the bottom; the release notes are taken from that section

## Stable release

```sh
git tag -a v0.x.y -m v0.x.y
git push origin v0.x.y
```

Release tags (`v*`) are protected against deletion, moving and force-pushes.

GitHub Actions then:

1. runs the tests on macOS; the release is blocked if they fail
2. builds `dok` for Apple Silicon and Intel on a macOS runner (the Keychain code needs cgo)
3. publishes the GitHub release with the archives, `checksums.txt` and its cosign signature, using the notes from `CHANGELOG.md`
4. updates the Homebrew formula `dok-for-confluence` in [nyactl/homebrew-tap](https://github.com/nyactl/homebrew-tap)

The workflow needs the repository secret `TAP_GITHUB_TOKEN`: a fine-grained token with "Contents: read and write" on `nyactl/homebrew-tap` only.

## Verifying release signatures

Every release is signed with [cosign](https://github.com/sigstore/cosign) using keyless signing via [Sigstore](https://sigstore.dev): the signature is bound to the release workflow of the exact tag, and no private key exists.

```sh
VERSION=v0.1.0

curl -LO https://github.com/nyactl/dok-for-confluence/releases/download/${VERSION}/checksums.txt
curl -LO https://github.com/nyactl/dok-for-confluence/releases/download/${VERSION}/checksums.txt.sigstore.json

cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/nyactl/dok-for-confluence/.github/workflows/release.yml@refs/tags/${VERSION}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  checksums.txt

curl -LO https://github.com/nyactl/dok-for-confluence/releases/download/${VERSION}/dok-for-confluence_${VERSION#v}_darwin_arm64.tar.gz
shasum -a 256 --check --ignore-missing checksums.txt
```

If both checks succeed, the archive was built from the tagged source by the release workflow.

## Pre-releases

Tags like `v0.2.0-rc.1` are published as pre-releases. GoReleaser does not update the Homebrew formula for them.
