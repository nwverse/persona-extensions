import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile, writeFile, mkdir, mkdtemp, rm } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
const { checkLock } = await import(pathToFileURL(resolve("scripts/check-lock.mjs")).href);

const canonical = async () => JSON.parse(await readFile("package-lock.json", "utf8"));
async function withLock(change, check) {
  await mkdir("build", { recursive: true });
  const dir = await mkdtemp(resolve("build/lock-regression-"));
  try {
    const lock = await canonical(); change(lock);
    const path = resolve(dir, "package-lock.json"); await writeFile(path, JSON.stringify(lock));
    await check(path);
  } finally { await rm(dir, { recursive: true, force: true }); }
}
test("canonical and staged locks contain only public npm sources and the approved SDK link", async () => {
  const checked = await checkLock("package-lock.json");
  assert.deepEqual(checked, { packages: 37, public_resolved: 34, local_sdk_links: 1 });
  assert.deepEqual(await checkLock("build/package/package-lock.json"), checked);
  assert.deepEqual(JSON.parse(await readFile("build/package/package-lock.json", "utf8")), await canonical());
});
test("34 unapproved resolved sources reproduce the provenance failure without embedding a private URL", async () => {
  await withLock(lock => {
    let count = 0;
    for (const pkg of Object.values(lock.packages)) if (pkg.resolved?.startsWith("https:")) {
      const url = new URL(pkg.resolved); url.hostname = "registry.example.invalid"; pkg.resolved = url.href; count++;
    }
    assert.equal(count, 34);
  }, path => assert.rejects(checkLock(path), /unapproved lock source/));
});
test("lock provenance rejects URL userinfo, query credentials and unapproved file links", async () => {
  for (const resolved of ["https://fixture:synthetic@registry.npmjs.org/example/-/example-1.0.0.tgz", "https://registry.npmjs.org/example/-/example-1.0.0.tgz?token=synthetic", "file:../../other-sdk"]) {
    await withLock(lock => { const pkg = Object.values(lock.packages).find(pkg => pkg.resolved?.startsWith("https:")); pkg.resolved = resolved; }, path => assert.rejects(checkLock(path), /unapproved lock source/));
  }
});
