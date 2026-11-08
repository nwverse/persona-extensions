#!/usr/bin/env python3
"""Two-phase OCI publication: push packages, then anonymously verify a saved plan for signing.

The read-only audit can run before cutover. All publication commands require the final repository identity.
"""
import argparse
import copy
import io
import os
import pathlib
import re
import subprocess
import sys
import tarfile
import tempfile

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import catalog
import manifest as manifests
import oci

FINAL_REPOSITORY = "nwverse/persona-extensions"
CLEANED_REPOSITORY_ID = 1449704212
TRUSTED_BRANCHES = {"main", "feat/146-oci-extension-market"}


def gh(*args):
    result = subprocess.run(["gh", *map(str, args)], capture_output=True, timeout=180)
    if result.returncode:
        raise ValueError("GitHub read operation failed: " + args[0])
    return result.stdout


def identity(repository, repository_id, ref):
    # Name alone is insufficient: the former repository must never publish to the new package namespace.
    if repository != FINAL_REPOSITORY or str(repository_id) != str(CLEANED_REPOSITORY_ID):
        raise ValueError("publication blocked until cleaned repository cutover is complete")
    if ref not in TRUSTED_BRANCHES:
        raise ValueError("publication requires trusted main or the issue-146 branch")


def cutover_gate(args):
    if not args.confirm_cutover:
        raise ValueError("explicit --confirm-cutover is required")
    # Check identity before credentials, downloads, builds or writes. API failures are not confirmation.
    identity(args.repository, args.repository_id, args.ref)
    remote = catalog.loads(gh("api", "repos/" + args.repository))
    identity(remote["full_name"], remote["id"], args.ref)


def archive_matches(data, package):
    if oci.digest(data) != "sha256:" + package["sha256"] or len(data) != package["size"]:
        raise ValueError("cleaned Release archive checksum or size mismatch")


def archive_manifest(data, extension_id):
    # Inspect only, never extract downloaded archives or run code inside them.
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        name = extension_id + "/persona-extension.json"
        matches = [m for m in archive.getmembers() if m.name == name]
        if len(matches) != 1 or not matches[0].isfile() or matches[0].size > 1024 * 1024:
            raise ValueError("archive has no unique regular extension manifest")
        return catalog.loads(archive.extractfile(matches[0]).read())


def check_archive(data, expected, strict=False):
    actual = archive_manifest(data, expected["id"])
    if actual.get("id") != expected["id"] or actual.get("version") != expected["version"]:
        raise ValueError("archive extension identity differs from catalog")
    if strict and actual != expected:
        raise ValueError("new-version archive manifest differs from source")
    commands = expected.get("runtime", {}).get("command", {})
    if actual.get("runtime", {}).get("command", {}) != commands:
        raise ValueError("archive runtime platform mapping differs from catalog")
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        members = archive.getmembers()
        for path in commands.values():
            full = expected["id"] + "/" + path
            matches = [m for m in members if m.name == full]
            if len(matches) != 1 or not matches[0].isfile():
                raise ValueError("archive is missing a regular platform binary")
    return sorted(k for k in actual.keys() | expected.keys() if actual.get(k) != expected.get(k))


def cleaned_archive(repository, entry, package, destination):
    m = entry["manifest"]
    platform = package.get("platform", "any")
    expected_name = oci.filename(m["id"], m["version"], platform)
    expected_tag = m["id"] + "-v" + m["version"]
    if package["url"] != f"release:{expected_tag}/{expected_name}":
        raise ValueError("bootstrap requires the frozen cleaned Release reference")
    remote = catalog.loads(gh("api", f"repos/{repository}/releases/tags/{expected_tag}"))
    assets = [a for a in remote["assets"] if a["name"] == expected_name]
    if len(assets) != 1 or assets[0].get("digest") != "sha256:" + package["sha256"] or assets[0]["size"] != package["size"]:
        raise ValueError("cleaned Release asset metadata differs from frozen signed catalog")
    # One fresh empty directory per downloaded archive; gh only downloads, nothing is executed here.
    directory = pathlib.Path(tempfile.mkdtemp(prefix=m["id"] + "-", dir=destination))
    gh("release", "download", expected_tag, "--repo", repository, "--pattern", expected_name, "--dir", directory)
    data = (directory / expected_name).read_bytes()
    archive_matches(data, package)
    # Cleaned historical archives can have scrubbed descriptive fields. The approved frozen signature
    # pins their exact bytes; never rebuild or rewrite them to make descriptive metadata match.
    differences = check_archive(data, m)
    return data, {"release_id": remote["id"], "tag": remote["tag_name"], "asset_id": assets[0]["id"],
                  "filename": expected_name, "sha256": package["sha256"], "size": len(data),
                  "runtime_platforms": sorted(m.get("runtime", {}).get("command", {})),
                  "manifest_field_differences": differences}


def audit(args):
    remote = catalog.loads(gh("api", "repos/" + args.repository))
    if remote["id"] != CLEANED_REPOSITORY_ID:
        raise ValueError("audit source is not the approved cleaned repository")
    _, body = catalog.verified(args.catalog)
    destination = pathlib.Path(args.output)
    destination.mkdir(parents=True, exist_ok=False)
    release_lines = gh("api", "--paginate", f"repos/{args.repository}/releases?per_page=100", "--jq",
                       '.[] | {id, tag_name, assets: [.assets[] | {id, name, size, digest}]}')
    releases = [catalog.loads(line) for line in release_lines.splitlines()]
    results = []
    for entry in body["extensions"]:
        for package in entry["packages"]:
            _, receipt = cleaned_archive(remote["full_name"], entry, package, destination)
            receipt["id"] = entry["manifest"]["id"]
            receipt["platform"] = package.get("platform", "any")
            results.append(receipt)
            print(receipt["id"] + ": cleaned Release bytes verified")
    # Preserve all historic identities for an independent before/after comparison, not just counts.
    history = [{"id": r["id"], "tag": r["tag_name"], "assets": [
        {k: a.get(k) for k in ("id", "name", "size", "digest")} for a in r["assets"]]} for r in releases]
    result = {"repository_id": remote["id"], "releases": len(releases),
              "assets": sum(len(r["assets"]) for r in releases), "current_packages": results, "history": history}
    (destination / "audit.json").write_bytes(catalog.encode(result))
    print(f"audit: ok ({len(results)} verified packages; {result['releases']} Releases; {result['assets']} assets)")


def desired_entries(body, mode):
    if mode == "bootstrap":
        # Do not derive already published versions from a new build or from the former repository.
        if len(body["extensions"]) != 14 or any(len(e["packages"]) != 1 or e["packages"][0].get("platform", "any") != "any"
                                                for e in body["extensions"]):
            raise ValueError("bootstrap expects the frozen 14 any-platform packages")
        return copy.deepcopy(body["extensions"])
    prior = {e["manifest"]["id"]: e for e in body["extensions"]}
    result = []
    for extension_id in manifests.ids():
        m = manifests.packaged(extension_id)
        old = prior.get(extension_id)
        if old and old["manifest"]["version"] == m["version"]:
            if old["manifest"] != m or old["tags"] != manifests.tags(extension_id):
                raise ValueError("same-version manifest or tags changed; bump version before publishing")
            continue
        result.append({"manifest": m, "tags": manifests.tags(extension_id), "packages": []})
    if result and any(not p["url"].startswith("oci:") for e in body["extensions"] for p in e["packages"]):
        raise ValueError("complete frozen Release bootstrap before publishing new versions")
    return result


def reuse_version(registry, entry):
    m = entry["manifest"]
    try:
        ref, data = registry.read_manifest(oci.version_tag(m["version"]))
    except oci.Missing:
        return None
    body = catalog.loads(data)
    layers = body.get("layers", [])
    if len(layers) != 1:
        raise ValueError("existing version must have exactly one archive layer")
    layer = layers[0]
    expected = oci.manifest(m["id"], m["version"], layer["digest"], layer["size"])
    if body != expected:
        raise ValueError("existing version tag metadata conflicts")
    registry.read_blob(expected["config"]["digest"], expected["config"]["size"])
    archive = registry.read_blob(layer["digest"], layer["size"])
    if archive_manifest(archive, m["id"]) != m:
        raise ValueError("existing version manifest conflicts with source; bump version")
    return {"url": oci.package_url(m["id"], ref), "sha256": layer["digest"].removeprefix("sha256:"), "size": layer["size"]}


def push(args):
    cutover_gate(args)
    token, username = os.environ.get("GHCR_TOKEN", ""), os.environ.get("GITHUB_ACTOR", "")
    if not token or not username:
        raise ValueError("GHCR_TOKEN and GITHUB_ACTOR are required")
    original, body = catalog.verified(args.catalog)
    entries = desired_entries(body, args.mode)
    output = pathlib.Path(args.output)
    output.mkdir(parents=True, exist_ok=False)
    plan = {"repository_id": CLEANED_REPOSITORY_ID, "head_sha": args.head_sha, "mode": args.mode,
            "base_payload_sha256": oci.digest(original), "entries": [], "release_receipts": []}
    for entry in entries:
        m = entry["manifest"]
        registry = oci.Registry(oci.repository(m["id"]), token, username)
        if args.mode == "bootstrap":
            packages = []
            for package in entry["packages"]:
                archive, receipt = cleaned_archive(args.repository, entry, package, output)
                ref = registry.push_once(m["id"], m["version"], archive, package.get("platform", "any"))
                packages.append(dict(package, url=oci.package_url(m["id"], ref)))
                plan["release_receipts"].append(receipt)
            entry["packages"] = packages
        else:
            package = reuse_version(registry, entry)
            if package is None:
                subprocess.run([str(catalog.ROOT / "scripts/package.sh"), m["id"]], check=True, timeout=900,
                               env={k: v for k, v in os.environ.items() if k not in ("GHCR_TOKEN", "GH_TOKEN", "EXTENSION_MARKET_SIGNING_KEY")})
                archive = (catalog.ROOT / "dist" / oci.filename(m["id"], m["version"])).read_bytes()
                if archive_manifest(archive, m["id"]) != m:
                    raise ValueError("new archive manifest does not match source")
                ref = registry.push_once(m["id"], m["version"], archive)
                package = {"url": oci.package_url(m["id"], ref), "sha256": oci.digest(archive)[7:], "size": len(archive)}
            entry["packages"] = [package]
        plan["entries"].append(entry)
        # Save after every version; a failed run's partial plan is evidence, never a signable catalog.
        (output / "plan.json").write_bytes(catalog.encode(plan))
    (output / "plan.json").write_bytes(catalog.encode(plan))
    settings = [{"package": oci.repository(e["manifest"]["id"]),
                 "settings_url": "https://github.com/orgs/nwverse/packages/container/" +
                                 "persona-extensions%2F" + e["manifest"]["id"] + "/settings"}
                for e in entries]
    (output / "package-settings.json").write_bytes(catalog.encode(settings))
    print("packages: ok; visibility is a separate authorized human operation")


def validate_plan(plan, original, body, head_sha):
    if plan.get("repository_id") != CLEANED_REPOSITORY_ID or plan.get("head_sha") != head_sha:
        raise ValueError("plan source identity or checkout SHA mismatch")
    if plan.get("base_payload_sha256") != oci.digest(original):
        raise ValueError("catalog moved since packages were published; generate a new plan")
    if plan.get("mode") not in ("bootstrap", "versions"):
        raise ValueError("unknown publication plan mode")
    expected = desired_entries(body, plan["mode"])
    entries = plan["entries"]
    if len(entries) != len(expected):
        raise ValueError("plan is incomplete")
    for actual, wanted in zip(entries, expected):
        if set(actual) != {"manifest", "tags", "packages"} or actual["manifest"] != wanted["manifest"] or actual["tags"] != wanted["tags"]:
            raise ValueError("plan manifest or tags differ from trusted checkout")
        if plan["mode"] == "bootstrap":
            if len(actual["packages"]) != len(wanted["packages"]):
                raise ValueError("bootstrap package count changed")
            for a, w in zip(actual["packages"], wanted["packages"]):
                if {k: v for k, v in a.items() if k != "url"} != {k: v for k, v in w.items() if k != "url"}:
                    raise ValueError("bootstrap archive descriptor differs from frozen signature")
        elif len(actual["packages"]) != 1 or set(actual["packages"][0]) != {"url", "sha256", "size"}:
            raise ValueError("new versions must have one any-platform archive")
    catalog.validate_payload(dict(body, extensions=entries))
    return entries


def generate_payload(plan, catalog_path, head_sha, registry_factory=oci.Registry):
    original, body = catalog.verified(catalog_path)
    entries = validate_plan(plan, original, body, head_sha)
    # Construct clients with NO credentials. Do not reuse the publisher's authenticated clients/tokens.
    for entry in entries:
        m = entry["manifest"]
        registry = registry_factory(oci.repository(m["id"]))
        for package in entry["packages"]:
            oci.verify_package(registry, m["id"], m["version"], package,
                               archive_validator=lambda data: check_archive(data, m, plan["mode"] == "versions"))
            print(m["id"] + ": anonymous manifest/config/archive verified")
    merged = {e["manifest"]["id"]: e for e in body["extensions"]}
    merged.update({e["manifest"]["id"]: e for e in entries})
    body["extensions"] = [merged[k] for k in sorted(merged)]
    # Verify every retained OCI reference too, not just the newly uploaded versions.
    for entry in body["extensions"]:
        if entry in entries:
            continue
        m = entry["manifest"]
        registry = registry_factory(oci.repository(m["id"]))
        for package in entry["packages"]:
            if not package["url"].startswith("oci:"):
                raise ValueError("bootstrap must finish before publishing new-version catalogs")
            oci.verify_package(registry, m["id"], m["version"], package,
                               archive_validator=lambda data: check_archive(data, m))
    return catalog.encode(catalog.validate_payload(body))


def catalog_phase(args):
    cutover_gate(args)
    plan = catalog.loads(pathlib.Path(args.plan).read_bytes())
    payload = generate_payload(plan, args.catalog, args.head_sha)
    output = pathlib.Path(args.output)
    output.mkdir(parents=True, exist_ok=False)
    (output / "payload.json").write_bytes(payload)
    print("anonymous gate: ok; payload ready for independent secret signing step")


def validate_run(args):
    cutover_gate(args)
    if not re.fullmatch(r"[0-9]+", args.run_id):
        raise ValueError("invalid package run id")
    run = catalog.loads(gh("api", f"repos/{args.repository}/actions/runs/{args.run_id}"))
    if (run.get("repository", {}).get("id") != CLEANED_REPOSITORY_ID or run.get("head_sha") != args.head_sha or
            run.get("head_branch") not in TRUSTED_BRANCHES or run.get("conclusion") != "success" or
            run.get("path") != ".github/workflows/ci.yml" or run.get("event") not in ("push", "workflow_dispatch")):
        raise ValueError("package artifact must come from a successful trusted same-SHA publication run")
    print("package run: approved provenance")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["audit", "push", "catalog", "validate-run", "gate"])
    parser.add_argument("--repository", default=os.environ.get("GITHUB_REPOSITORY", "nwverse/persona-extensions-next"))
    parser.add_argument("--repository-id", default=os.environ.get("GITHUB_REPOSITORY_ID", ""))
    parser.add_argument("--ref", default=os.environ.get("GITHUB_REF_NAME", ""))
    parser.add_argument("--head-sha", default=os.environ.get("GITHUB_SHA", ""))
    parser.add_argument("--confirm-cutover", action="store_true")
    parser.add_argument("--catalog", default=catalog.CATALOG)
    parser.add_argument("--mode", choices=["bootstrap", "versions"], default="bootstrap")
    parser.add_argument("--output")
    parser.add_argument("--plan")
    parser.add_argument("--run-id")
    args = parser.parse_args()
    if args.command in ("audit", "push", "catalog") and not args.output:
        parser.error("--output is required")
    if args.command != "audit" and not re.fullmatch(r"[0-9a-f]{40}", args.head_sha):
        parser.error("publication requires a full trusted checkout SHA")
    if args.command == "audit":
        audit(args)
    elif args.command == "push":
        push(args)
    elif args.command == "catalog":
        if not args.plan:
            parser.error("--plan is required")
        catalog_phase(args)
    elif args.command == "validate-run":
        validate_run(args)
    else:
        cutover_gate(args)
        print("cutover identity gate: ok")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, TypeError, KeyError, oci.Missing, subprocess.SubprocessError) as error:
        # Subprocess diagnostics/registry responses must not leak credentials.
        detail = str(error) if not isinstance(error, subprocess.SubprocessError) else "external operation failed"
        sys.exit("publisher: failed: " + detail)
