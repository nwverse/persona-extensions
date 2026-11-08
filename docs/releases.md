# Releases and market signing

[Documentation](README.md) · [Installation](installation.md)

Publishing packages, changing package visibility and dispatching publication workflows require authorization. Code and tests can merge after their own CI passes **before** cutover, but publication cannot. The cutover owner handles repository renaming and visibility; this workflow does neither. Local CI and synthetic fixtures are not evidence of a published package or a working hub installation.

## Market and indexes

The repository has one catalog, `persona-extensions.json`, with embedded signatures:

```json
{
  "payload": "<the original UTF-8 catalog JSON string, including its whitespace>",
  "signatures": [
    {"alg": "ed25519", "key_id": "3cce0f471911a1cf", "sig": "<base64 signature>"}
  ]
}
```

The payload has `market_version: 1`, market metadata, `signing_key` and `extensions`. Each entry contains its packaged `manifest` (without `tags`), entry `tags` and `packages`. There is no legacy catalog, detached signature or standalone public-key file. [scripts/catalog.py](../scripts/catalog.py) retains the approved **public** key as a code constant; no private key belongs in the repository.

Signatures cover `payload.encode("utf-8")`, **not** the outer JSON, parsed objects or canonicalized JSON. Outer formatting can change; the signed string cannot. Duplicate keys, absent/empty signatures, the wrong key and malformed signatures fail closed.

The initial format conversion deliberately retains the frozen signed payload's `release:<tag>/<filename>` references. It is not an OCI publication. Do not replace these with guessed digests. Only successful package publication and the anonymous gate below generate OCI references.

## OCI contract

| Part | Contract |
| --- | --- |
| Package path | `ghcr.io/nwverse/persona-extensions/<id>:<version>` (no `latest`) |
| Catalog URL | `oci:ghcr.io/nwverse/persona-extensions/<id>@sha256:<manifest digest>` |
| Manifest | OCI image manifest, `schemaVersion: 2`, media type `application/vnd.oci.image.manifest.v1+json` |
| Artifact type | `application/vnd.persona.extension.v1` |
| Config | Empty `{}` blob, `application/vnd.oci.empty.v1+json` |
| Layer | Exactly one archive; `application/vnd.persona.extension.package.v1+gzip` |
| Title | `org.opencontainers.image.title`, safe `<id>-<version>.tar.gz` filename |
| Source | Always `https://github.com/nwverse/persona-extensions`, including before the source repository is renamed |
| Catalog `sha256` / `size` | Archive **blob** digest / bytes, not manifest digest / bytes |

Current packages are all `any`: each program archive includes its manifest's platform-to-binary map. Declarative packages need no binary. Explicit platform packages keep their `platform` field, use `<version>-<platform>` tags and safe platform-specific archive titles; manifests are not Docker multi-platform indexes.

[scripts/oci.py](../scripts/oci.py) implements the exact Distribution protocol with Python's standard library. It checks manifest/config/archive digests and metadata. Existing tags are verified and reused, never overwritten; an archive, metadata or source-label conflict requires a new version. Only a registry HTTP 404 means absent. Auth, rate limits, transport and server errors stop publication. Workflow writers are serialized globally, across trusted branches. Never run an independent tag writer concurrently.

## Automated releases

[CI](../.github/workflows/ci.yml) runs [scripts/ci.sh](../scripts/ci.sh) on PRs, main pushes, the issue-146 branch and dispatches. `CI required` fails unless PR checks succeed. Local checks do not replace required hosted CI. `scripts/ci.sh` never publishes and rejects the former `release` arguments.

Publication requires both the final name `nwverse/persona-extensions` and cleaned repository ID `1449704212`. The former repository cannot pass this gate. Only trusted `main` and `feat/146-oci-extension-market` refs can publish. Before cutover, pushes perform **checks only**; dispatch publication refuses even when a caller checks the confirmation input. The script rechecks the actual GitHub repository identity before reading package credentials or doing any upload.

After the cutover owner confirms completion, an authorized maintainer can enable future push publication with the repository variable `PERSONA_OCI_CUTOVER_READY=true`. A dispatch additionally requires `confirm_cutover=true`. This variable is not set by the workflow. Do not enable it or dispatch early.

### Phase 1: packages

The initial bootstrap downloads **all 14 current archives from the cleaned repository's Releases**, compares asset metadata and downloaded bytes to the frozen signed catalog, and uploads those exact bytes. It never recompiles/replaces a current version or uses the former repository's assets. Cleaned historical archives can differ from the catalog in scrubbed descriptive fields; the audit lists those differences while enforcing identical id/version/runtime mappings and exact frozen checksums. Do not rewrite them to make their descriptions match.

Future main pushes select source manifest versions not already in the signed catalog, including work from a replaced waiting run. A same-version manifest/tags change fails. An existing registry version is verified and reused **before any build**, so interrupted runs do not rebuild or overwrite published bytes. New versions are locally packaged once if absent. There are no Release/tag creation/deletion calls. The current catalog must first complete bootstrap before new-version catalogs can be generated.

The successful job produces artifact **`oci-package-plan`**:

- `plan.json`: source SHA, frozen payload digest, complete updated entries, OCI manifest references, archive descriptors and cleaned Release receipts for bootstrap.
- `package-settings.json`: all package names and GitHub package settings URLs for that run.

A failed run may retain a partial plan for diagnosis. It cannot pass the successful-run provenance check or completeness validation. Rerun packages to resume from verified registry versions. Do not sign a partial plan.

### Human visibility step

New GHCR packages are private by default, even when the source repository is public. An authorized, logged-in organization owner opens every URL in `package-settings.json` and changes **Package settings → Danger Zone → Change visibility → Public**. There is no documented REST endpoint to change GHCR visibility; this workflow does not pretend to have one, create credentials or log in for the owner.

Do this for the complete bootstrap batch, and for every newly introduced package later. Existing public packages' new versions normally need no visibility change.

### Phase 2: catalog

Dispatch `phase=catalog` using the successful package run id and the **same checkout SHA**. The workflow checks repository/run identity, trusted branch, workflow path, successful conclusion and matching SHA before downloading the plan. If main moved, rerun packages at the new SHA; existing tags are reused.

A separate job creates fresh **anonymous** registry clients, ignoring all GitHub/registry credential environment variables. It checks every retained and updated OCI package by digest, including config, archive, id/version and platform binaries. Private packages, wrong digests, a changed source catalog or incomplete/tampered plan block payload output. Archive checks are bounded; registry token requests use the fixed GHCR HTTPS endpoint and exact pull scope. Credentials never follow blob redirects or a server-supplied realm.

Only after this gate does artifact **`verified-unsigned-payload`** contain `payload.json`. A separate signing job downloads it; no extension build/test code runs in that job. Only its signing step receives `EXTENSION_MARKET_SIGNING_KEY`. Missing secret, a secret exporting a different public key or failed verification is fatal. The fixed key remains `3cce0f471911a1cf`; embedding signatures is **not** another rotation.

Artifact **`signed-oci-catalog`** contains `persona-extensions.json` and `PR-body.txt` (`Refs #146`). An authorized maintainer applies the verified catalog in an issue-linked PR, not a direct main commit. Require that PR's own green CI before merging. A catalog-only main merge selects no new versions and writes no packages; there is no catalog-PR publication loop and no bypass deploy key.

## Authorized cutover bootstrap

These are instructions for **after** the cutover owner's confirmation, not commands to run while the repository is still next. Do not change repository names or visibility here.

```sh
# At an approved trusted ref/SHA, first push exact cleaned current archives.
gh workflow run ci.yml --repo nwverse/persona-extensions --ref main \
  -f phase=packages -f mode=bootstrap -F confirm_cutover=true

# After that run succeeds and a logged-in owner makes every listed package public:
# use the same ref/SHA as the package run (or rerun packages if main moved).
gh workflow run ci.yml --repo nwverse/persona-extensions --ref main \
  -f phase=catalog -f package_run_id=<successful-package-run-id> -F confirm_cutover=true

# Inspect the signed output in a new empty directory; do not execute downloaded code.
gh run download <catalog-run-id> --repo nwverse/persona-extensions \
  -n signed-oci-catalog -D /path/to/new-empty-catalog-directory
python3 -I scripts/sign-index.py verify \
  --catalog /path/to/new-empty-catalog-directory/persona-extensions.json
```

Apply that one catalog file to a new issue-linked branch/PR. Do not copy a workflow, script, old public key or historical package from the artifact. The PR body must reference #146. Release/tag identities and all archive digests remain unchanged; compare the audit history before/after, not only counts.

For an independently authorized local publisher, [scripts/release.sh](../scripts/release.sh) delegates to the same identity-gated `push` command. Provide `--repository`, `--repository-id`, `--ref`, full `--head-sha`, `--confirm-cutover`, `--mode` and a new `--output` directory. GHCR credentials belong in `GHCR_TOKEN`/`GITHUB_ACTOR`, never argv or repository files. Signing still belongs in the independent Actions secret step, not local bootstrap.

## Market signing

The approved cleaned repository's current key id is `3cce0f471911a1cf`. The Persona hub's official pin and sandbox allowlist must use the corresponding public key too; hub pin migration is tracked separately in nwverse/persona#1157. A synthetic test is not proof that a deployed hub has the new pin. Preserve deliberate/unknown user pins, do not recreate deleted markets, and quarantine cached catalogs until real re-verification succeeds. Do not instruct users to bypass verification with the removed non-official TOFU/trust flow.

The private key already lives in the repository Actions secret `EXTENSION_MARKET_SIGNING_KEY`. Keep it there and do not rotate it for this format change. Its exported public key must match [scripts/catalog.py](../scripts/catalog.py). A lost/compromised key requires a separately authorized recovery decision, not an automatic key generator or silent fallback.

## Verification without a private key

```sh
python3 -I scripts/index.py check
python3 -I scripts/sign-index.py verify
```

Both require a present valid embedded signature. The initial conversion used the following tool while the original next files were present:

```sh
python3 -I scripts/sign-index.py convert-legacy
```

`convert-legacy` verifies the original raw file with its original detached signature and approved public key **before** wrapping the original UTF-8 string and reusing that same signature. It does not read a private key or re-sign. Custom `--catalog`, `--signature`, `--public-key` and `--output` paths allow independent conversion checks of a frozen original snapshot. Never recreate removed detached files on main.

## Read-only cleaned Release audit

This can run before cutover; it does not upload, sign, dispatch or change visibility:

```sh
python3 -I scripts/publish-oci.py audit \
  --repository nwverse/persona-extensions-next \
  --output /path/to/new-empty-audit-directory
```

After renaming, pass the final repository name instead; the immutable cleaned repository id is checked. Each downloaded archive gets its own fresh directory and is only read, never extracted/executed. `audit.json` lists the current exact-byte checks, platform mappings, descriptive-field differences and historical Release/asset identities. Do not include credentials in audit logs or commit downloaded archives. Keep audit evidence separate from code and do not mistake it for an anonymous OCI or real hub installation test.
