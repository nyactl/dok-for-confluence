# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## Unreleased

### Added

- Read pages as Markdown, list child pages, comments and attachments, and search the allowed spaces, from the CLI and as MCP tools.
- Create pages (drafts unless published right away), publish drafts, update pages with a version check and a diff, add comments with mentions, and attach files; every change needs approval in the terminal or an MCP approval dialog.
- Space allowlist (`dok config spaces`) and optional write limits to page trees (`dok config parents`).
- Privacy mode `own`: other people appear as "Person A", "Person B".
- Scoped API tokens through the Atlassian API gateway, with the missing scope named in errors.
- Profiles, and development builds that only use profiles that opted in.
