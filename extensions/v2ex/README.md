# V2EX

Read public V2EX topics in Persona, without an account: hot, latest and followed-node lists, an in-app topic reader and assistant tools.

- [Extension guide and configuration](../../docs/extensions/v2ex.md)
- [Installation](../../docs/installation.md)
- [Development and tests](../../docs/development.md)
- [Manifest](persona-extension.json)

## Unreleased source profile

The TypeScript Program shares a bounded service between connector tools and the
native `topics` page. Existing settings, source lists, contribution IDs, grants,
localizations and request deadlines remain unchanged. UI versions below 3 open a
topic inline; later versions push a native topic session and retain the parent.
Stable keys, cached resumes and replayable native callback receipts are preserved.
Lists page through all 200 retained topics without increasing aggregate frame limits.

From this extension directory, run:

```sh
npm --prefix ../../sdk/ts ci --ignore-scripts --registry=https://registry.npmjs.org
npm ci --ignore-scripts --registry=https://registry.npmjs.org
npm run typecheck
npm test
npm run pack:source
```

Packaging produces `build/v2ex-source.tar.gz`. `build/source.json` records exact
bundle/archive hashes and byte counts. The import-free bundle exports the default
Program; its staged manifest describes Deno source rather than a Go executable.
Only the packaged manifest contains the final bundle hash, avoiding a self-hash
cycle. The official SDK authoring checker runs offline during packaging; it is
not an installed-hub or source-confinement test.

Rebuild the contained archive from `authoring/extensions/v2ex/`, not its reference
source files at the archive root. The `file:../../sdk/ts` dependency resolves inside
that subtree to the unmodified official SDK module, including its checker workers.
Public-registry lockfiles are checked independently. No package default, released
Go file, manifest, version, catalog, signature or publication is changed.

### Authoring boundaries

HTTP responses and raw page state retain signed-int64 values, numeric spelling,
ordered duplicate errors and RawMessage deletion flags after syntax validation.
An iterative lossless parser accepts ignored deep fields without overflowing the
JavaScript call stack. Keyword lowercase is context-free like Go; field matching
uses simple folding, and configured node names use Go's Unicode White_Space.
Extended date and folding fixtures retain their existing Go comparison evidence;
this source delivery does not regenerate the original-Go oracle.

The connector uses the official, lazily bound exact-params handler. It preserves
full signed-int64 lexical/range rules and scalar versus pointer null behavior,
then clamps counts before converting them to JavaScript numbers. It does not use
a safe-integer subset or reconstruct original tokens from rounded numbers.
Finite page loading opts into `useLoadEffect`, so snapshot host calls await the
bounded read before suspension. Direct Renderer suspension before completion is
still retained as a failing negative control, not proof about the installed host.

**Initializer raw configuration remains blocked.** The official `Init` exposes
an already parsed config object, so an earlier invalid duplicate can be lost
before this extension sees it. For example, `{"nodes":1,"nodes":["go"]}` must fail
like Go, but currently initializes. A real failing TODO records this gap; ordinary
`JSON.parse` is not claimed to solve it. `npm test` reports `BLOCKED` and exit 2
while any TODO actually fails. Exact connector support does not cover initialize.

The SDK dependency is provisional until the official upstream-main synchronization
is ready. Hosted required CI, coordinator-owned isolated installed-hub acceptance,
actual client readings and screenshots remain merge gates. Synthetic fixtures and
offline checks are not installation acceptance. No production activation or release
is part of this delivery.
