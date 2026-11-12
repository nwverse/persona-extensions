import { test } from "node:test";
import assert from "node:assert/strict";
import { storageBytes, storageValue } from "@persona/extension";
import { decodeList, ListeningStore } from "../src/store.ts";
import { parseShow, ReadError } from "../src/model.ts";
import { sampleShow } from "./fixtures.ts";
class FakeHub {
  data: string | undefined;
  revision = 0;
  requests = 0;
  reads = 0;
  conflict = false;
  calls: { method: string; params: any }[] = [];
  async call<T>(method: string, params: any): Promise<T> {
    this.calls.push({ method, params });
    if (method === "storage.get") {
      this.reads++;
      await new Promise(resolve => setTimeout(resolve, 2));
      if (!this.data) throw Object.assign(new Error("missing"), { code: "not_found" });
      return { revision: `r${this.revision}`, data: this.data } as T;
    }
    if (method === "storage.request") return { request_id: `request-${++this.requests}` } as T;
    if (method === "storage.transaction") {
      assert.equal(params.request_id, `request-${this.requests}`);
      const operation = params.operations[0];
      assert.equal(operation.key, "subscriptions");
      if (this.conflict || operation.revision !== (this.revision ? `r${this.revision}` : "")) throw Object.assign(new Error("conflict"), { code: "storage_conflict" });
      decodeList(operation.data);
      this.data = operation.data; this.revision++;
      return { items: [{ key: "subscriptions", revision: `r${this.revision}` }] } as T;
    }
    throw new Error("unexpected method");
  }
}
test("local store uses host request IDs, CAS revisions and no cross-extension authority", async () => {
  const hub = new FakeHub(), store = new ListeningStore(hub), show = parseShow(sampleShow);
  assert.deepEqual((await store.load()).shows, []);
  await store.save(show);
  assert.equal(store.has(show.id), true);
  const first = hub.calls.at(-1)!;
  assert.equal(first.params.operations[0].revision, "");
  await store.remove(show.id);
  assert.equal(store.has(show.id), false);
  const second = hub.calls.at(-1)!;
  assert.equal(second.params.operations[0].revision, "r1");
  assert.equal(new TextDecoder().decode(storageValue(hub.data!)), '{"schema_version":1,"shows":[]}');
  assert.equal(hub.requests, 2);
  assert.ok(hub.calls.every(call => ["storage.get", "storage.request", "storage.transaction"].includes(call.method)));
  const restored = new ListeningStore(hub);
  assert.deepEqual((await restored.load()).shows, []);
});
test("concurrent library reads coalesce and concurrent mutations stay serial", async () => {
  const hub = new FakeHub(), store = new ListeningStore(hub), show = parseShow(sampleShow);
  await Promise.all([store.load(), store.save(show)]);
  assert.equal(hub.reads, 1);
  assert.equal(store.has(show.id), true);
  assert.equal(hub.requests, 1);
  await Promise.all([store.remove(show.id), store.save(show)]);
  assert.equal(hub.requests, 3);
  assert.equal(store.has(show.id), true);
  assert.equal(decodeList(hub.data!).shows.length, 1);
});
test("CAS conflicts retain committed state and reject the stale mutation", async () => {
  const hub = new FakeHub(), store = new ListeningStore(hub);
  await store.save(parseShow(sampleShow));
  hub.conflict = true;
  await assert.rejects(store.remove(sampleShow.collectionId), (error: unknown) => error instanceof Error && (error as { code?: string }).code === "storage_conflict");
  assert.equal(store.has(sampleShow.collectionId), true);
  assert.equal(decodeList(hub.data!).shows.length, 1);
  hub.conflict = false;
  await store.remove(sampleShow.collectionId);
  assert.equal(decodeList(hub.data!).shows.length, 0);
});
test("a CAS conflict reloads the committed list and retries once", async () => {
  const hub = new FakeHub(), first = new ListeningStore(hub), second = new ListeningStore(hub);
  const one = parseShow(sampleShow), two = parseShow({ ...sampleShow, collectionId: 42, collectionName: "Another Show" });
  await first.save(one);
  await second.load();
  await first.save(two);
  await second.remove(one.id);
  assert.deepEqual((await second.load()).shows.map(show => show.id), [two.id]);
  assert.deepEqual(decodeList(hub.data!).shows.map(show => show.id), [two.id]);
  assert.equal(hub.requests, 4);
});
test("storage bytes remain bounded and contain no media", async () => {
  const show = parseShow(sampleShow);
  const bytes = storageBytes(new TextEncoder().encode(JSON.stringify({ schema_version: 1, shows: [show] })));
  assert.ok(bytes.length < 2000);
  assert.ok(!bytes.includes("audio"));
  assert.throws(() => storageBytes(new Uint8Array(65_537)), RangeError);
  assert.throws(() => storageValue("YQ"), TypeError);
});
