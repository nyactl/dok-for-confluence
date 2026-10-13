# Integration tests

These tests run dok against a real Confluence Cloud site. They create a page (first as a draft, then published), change it, comment on it and attach a file, so **use a dedicated test site and space**, never a production one. dok cannot delete pages: remove the test pages in Confluence when you no longer need them.

## One-time setup

1. Create a free Atlassian Cloud site with Confluence at <https://www.atlassian.com/software/confluence/free>, and a space for the tests. Note its key, shown in the space's URL (`/wiki/spaces/KEY/...`).
2. Log an account of that site into a separate dok profile, so your real setup stays untouched. A scoped token with the scopes listed in the main README works best:

   ```sh
   dok --profile test auth login
   dok --profile test config spaces add KEY
   ```

3. Allow development builds and the tests to use this profile, and only this one:

   ```sh
   dok config dev-builds on --profile test
   ```

   The tests refuse the default profile, profiles that did not opt in, and any profile on the same site as the default profile, so they cannot reach a production site by mistake.

## Run

```sh
DOK_PROFILE=test DOK_IT_SPACE=KEY make it
```

Set `DOK_IT_PARENT` to a page ID to create the test pages below that page instead of at the top of the space.

The first run of a freshly built test binary asks macOS for permission to read the token from the Keychain; choose "Always Allow".
