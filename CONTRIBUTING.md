# Contributing

Thanks for helping improve dok. Bug reports, fixes and focused feature proposals are welcome.

## Before you start

- For anything bigger than a small fix, open an issue first to agree on the approach.
- Security problems go through private reporting, see [SECURITY.md](SECURITY.md).
- By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).

## Development

Requires macOS (for the Keychain code) and Go 1.26 or newer.

```sh
git clone https://github.com/nyactl/dok-for-confluence.git
cd dok-for-confluence
make test    # unit tests
make lint
make dev     # installs dok-dev; use it with --profile test
make it      # integration tests against a test site, see integration/README.md
```

Unit tests must not contact a real Confluence site: use `internal/confluencetest`, the in-memory fake. Changes to Confluence API usage should also be checked with the integration suite.

## Rules that keep dok safe

Read [docs/design.md](docs/design.md) first. In short:

- Only `internal/core` touches raw Confluence data. It returns views with people already replaced; never print or return `confluence` types directly.
- Every page access goes through the allowlist check (`page`), and every write through `writable`.
- Every write is a `core.Plan` whose description states exactly what will change, confirmed through the terminal (CLI) or the approval gate (MCP).
- New MCP tools need correct `readOnlyHint` / `destructiveHint` annotations; write tools are registered only with `--allow-writes`.
- Build strings with invisible characters in tests with escapes such as `\u200b`, never as raw characters.
- New dependencies, tools and GitHub Actions are checked against their official sources before use.

## Commits and pull requests

- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/); mark breaking changes with `!`.
- Keep pull requests focused; tests, lint and govulncheck must pass.
- Add a line to the "Unreleased" section of [CHANGELOG.md](CHANGELOG.md) for user-visible changes. Releases are described in [RELEASING.md](RELEASING.md).
