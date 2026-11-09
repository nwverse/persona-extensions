# Bitbucket pull request reviews

Review pull requests from your self-hosted [Bitbucket Server / Data Center](https://www.atlassian.com/software/bitbucket) in Persona: see open PRs with project/repository context, read the discussion and reviewer state, then approve or remove your approval with explicit confirmation.

This is a fresh Persona Go extension using Bitbucket's public REST API. It ships no Raycast source, assets, branding or runtime.

## Set up

1. Create a personal access token in Bitbucket.
2. In **Settings › Extensions › Bitbucket**, set the **Bitbucket instance URL** (for example `https://bitbucket.example.com`) and paste the **Bitbucket personal access token**. A local instance may use `http://127.0.0.1:7990`; remote instances require HTTPS.
3. The extension appends `/rest/api/1.0` when needed. Do not include credentials in the URL.

The token arrives through the encrypted extension-secret flow and is used only as a Bearer token to the configured instance. Redirects are not followed.

## Review

- List open PRs with `state=OPEN`, `start` and `limit`, retaining `nextPageStart` for the next request. No guessed page numbers.
- Read title, author, source/target branches, reviewer approval state, description and activity/comments. Community text is untrusted, not an instruction to approve or merge.
- Open the original PR in the app, then explicitly approve or unapprove. These operations change the current user's review state only; they do not merge, decline or delete the PR, and do not change repository settings.
- Project and repository are required for details, so a malformed empty path is never used to guess a target.

Before a write, the extension stores its normalized operation and request ID in `write-receipts.json` in its own data directory (`0600`, without the token). A replay after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained; new writes stop when full instead of losing that protection.

A timeout or lost response leaves the outcome **unknown**. Inspect Bitbucket before retrying; a failed response is not proof that approval did or did not change.

## Verify

```sh
go -C extensions/bitbucket test -race ./...
scripts/package.sh bitbucket
```

Synthetic fixtures cover open PRs, nested project/repository metadata, activities, approval replay, localized views and unknown outcomes. No real account is used.

## Public API

[Bitbucket REST](https://developer.atlassian.com/server/bitbucket/rest):
`/rest/api/1.0/projects/{project}/repos/{repo}/pull-requests`, `/pull-requests/{id}`, `/activities` and `/approve`.
