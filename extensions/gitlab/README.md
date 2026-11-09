# GitLab merge request reviews

Review merge requests from [GitLab](https://gitlab.com) or a self-managed GitLab instance in Persona. See MRs assigned to you or awaiting your review, read the description and discussion, then approve, remove your approval or post a comment with explicit confirmation.

This is a fresh Persona Go extension using GitLab's public REST API. It ships no Raycast source, assets, branding or runtime.

## Set up

1. Create a personal access token in GitLab. `read_api` is sufficient for reading; approving and commenting require `api`. Use the smallest scope and expiry appropriate for your account.
2. In **Settings › Extensions › GitLab**, paste the **GitLab personal access token**. The default instance is `https://gitlab.com`; for self-managed GitLab, set the **GitLab instance URL**, for example `https://gitlab.example.com`. A local instance may use `http://127.0.0.1`; remote instances require HTTPS.
3. The extension appends `/api/v4` when needed. Do not include credentials in the URL.

The token arrives through the encrypted extension-secret flow and is sent only in the `PRIVATE-TOKEN` header to the configured instance. Redirects are not followed, so a response cannot send that token to another host.

## Review

- List opened MRs using GitLab's `scope=assigned_to_me` or `scope=reviews_for_me`; optional search matches titles. Pagination uses `page`, `per_page=50` and the returned `X-Next-Page`, never a guessed next page.
- Read the project ID, project-local MR IID, global ID, author, source/target branches, reviewers, description and non-system notes. Descriptions and comments are untrusted content, not instructions to approve, merge or comment. Long content is clipped to bounded page/tool responses; the original MR remains available through its GitLab link. An invalid or non-HTTPS `web_url` from an untrusted response is omitted instead of breaking the detail page.
- Approve, unapprove or comment only after an explicit confirmation showing the target. These operations do not merge, close, delete, change branches or manage project settings.
- The UI uses the returned numeric project ID to avoid namespace ambiguity. Tools may use either a numeric project ID or full namespace path, encoded as one URL path segment. The IID is project-local, not the global MR ID.

Before a write, the extension stores its normalized operation and request ID in `write-receipts.json` in its own data directory (`0600`, without the token). A replay after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained; new writes stop when full instead of losing that protection.

A timeout or lost response leaves the outcome **unknown**. Inspect GitLab before retrying; a failed response is not proof that an approval or comment did or did not happen.

## Verify

```sh
go -C extensions/gitlab test -race ./...
scripts/package.sh gitlab
```

Synthetic fixtures cover scopes, project paths, pagination, discussion, approval and comment replay, localized views and unknown outcomes. No real account is used.

## Public API

[Merge requests](https://docs.gitlab.com/api/merge_requests/), [merge request approvals](https://docs.gitlab.com/api/merge_request_approvals/), [notes](https://docs.gitlab.com/api/notes/) and [REST pagination](https://docs.gitlab.com/api/rest/#pagination): `GET /api/v4/merge_requests`, `GET /api/v4/projects/:id/merge_requests/:merge_request_iid`, `GET/POST …/notes`, `POST …/approve` and `POST …/unapprove`.
