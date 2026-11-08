import { test } from "node:test";
import assert from "node:assert/strict";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { connect, type Program } from "@persona/extension";
import { Fixture, interaction, rawTopic, tick } from "./fixtures.ts";

async function setup() {
  const bundle = await import(pathToFileURL(resolve("build/v2ex.mjs")).href), fixture = new Fixture(), frames: any[] = [];
  fixture.put("/topics/hot.json", Array.from({ length: 40 }, (_, i) => rawTopic(i + 1)));
  fixture.answers.set("/topics/show.json?id=9223372036854775807", { body: '[{"id":9223372036854775807}]' });
  const p = connect(bundle.createProgram(fixture.fetch, { limits: { gap: 0 } }) as Program, line => frames.push(JSON.parse(line)));
  const raw = async (id: string, method: string, params = "{}") => {
    p.line(`{"id":"${id}","method":"${method}","params":${params}}`); await p.drain();
    return frames.find(frame => frame.id === id);
  };
  await raw("init", "initialize", '{"protocol":1,"hub":{"locale":"en","timezone":"UTC"}}');
  return { p, raw, fixture };
}

test("lazy exact tool handlers retain signed-int64 tokens, ordered errors and null rules", async () => {
  const { p, raw, fixture } = await setup();
  try {
    let i = 0;
    for (const args of [
      '{"limit":1.00000000000000001}', '{"limit":1e0}', '{"limit":9223372036854775808}',
      '{"limit":-9223372036854775809}', '{"limit":"1","limit":1}', '{"LIMIT":true,"limit":1}',
      '{"replies":1.0,"replies":1}', '{"replies":[],"replies":null}',
    ]) {
      const hits = fixture.requests.length, result = await raw(`bad${i++}`, "connector.tools.call", `{"name":"v2ex_hot","args":${args}}`);
      assert.ok(result.error); assert.equal(fixture.requests.length, hits);
    }
    for (const [args, count] of [
      ['{"limit":9223372036854775807}', 30], ['{"limit":9007199254740993}', 30],
      ['{"limit":-9223372036854775808}', 15], ['{"limit":2,"LIMIT":null}', 2],
      ['{"limit":2,"limit":3}', 3], ['{"limit":null}', 15],
    ] as const) {
      const result = await raw(`good${i++}`, "connector.tools.call", `{"Name":"v2ex_hot","Args":${args}}`);
      assert.equal(result.error, undefined); assert.equal((result.result.text.match(/\(id /g) ?? []).length, count);
    }
    for (const args of [
      '{"topic":"invalid","topic":9223372036854775807,"replies":0}',
      '{"topic":9223372036854775807,"replies":-9223372036854775808}',
    ]) {
      const result = await raw(`topic${i++}`, "connector.tools.call", `{"name":"v2ex_topic","args":${args}}`);
      assert.equal(result.error, undefined); assert.ok(result.result.text.includes("9223372036854775807"));
    }
    for (const [args, count] of [
      ['{"topic":1,"replies":1,"replies":null}', 10], ['{"topic":1,"replies":null,"replies":1}', 1],
      ['{"topic":1,"replies":9223372036854775807}', 25],
    ] as const) {
      const result = await raw(`replies${i++}`, "connector.tools.call", `{"name":"v2ex_topic","args":${args}}`);
      assert.equal(result.error, undefined); assert.equal((result.result.text.match(/\n#\d+ /g) ?? []).length, count);
    }
    for (const params of ['{"name":1,"name":"v2ex_hot"}', '{"contribution":1,"contribution":"v2ex","name":"v2ex_hot"}']) {
      assert.ok((await raw(`envelope${i++}`, "connector.tools.call", params)).error);
    }
    assert.equal((await raw("missing-args", "connector.tools.call", '{"name":"v2ex_hot"}')).error, undefined);
  } finally { p.close(); }
});

test("finite snapshot loading returns topics before suspend and preserves cached receipts on resume", async () => {
  const { p, raw, fixture } = await setup();
  fixture.put("/topics/hot.json", [rawTopic()], { delay: 100 });
  const call = async (id: string, method: string, params: unknown) => {
    const frame = await raw(id, method, JSON.stringify(params)); assert.equal(frame.error, undefined); return frame.result;
  };
  try {
    const snapshot = await call("mount", "ui.page.mount", { session: "finite", transport: "snapshot", params: { contribution: "topics", ui_version: 1 } });
    assert.ok(JSON.stringify(snapshot.view).includes("topic-1")); assert.equal(fixture.requests.length, 1); assert.equal(fixture.cancelled, 0);
    await call("suspend", "ui.page.suspend", { session: "finite" });
    const resume = await call("resume", "ui.page.resume", { session: "finite" }); await tick(10);
    const settled = await call("settled", "ui.page.snapshot", { session: "finite" });
    const keys = (value: any) => value.view.sections.flatMap((section: any) => section.items.map((item: any) => item.key));
    assert.equal(settled.version, resume.version); assert.deepEqual(keys(resume), keys(snapshot)); assert.equal(fixture.requests.length, 1);
    const event = { session: "finite", ticket: "fixture-ticket", interaction: interaction(resume, "topic-1") };
    const first = await call("event", "ui.page.event", event), replay = await call("replay", "ui.page.event", event);
    assert.equal(first.status, "accepted"); assert.deepEqual(replay, first);
  } finally { p.close(); }
});

test("initializer raw-config type errors survive overwritten duplicate keys", { todo: "BLOCKED: official Init exposes parsed config, not original config tokens" }, async () => {
  const bundle = await import(pathToFileURL(resolve("build/v2ex.mjs")).href), frames: any[] = [];
  const fixture = new Fixture(), p = connect(bundle.createProgram(fixture.fetch) as Program, line => frames.push(JSON.parse(line)));
  try {
    p.line('{"id":"init","method":"initialize","params":{"protocol":1,"config":{"nodes":1,"nodes":["go"]}}}'); await p.drain();
    assert.ok(frames.find(frame => frame.id === "init").error, "ordinary JSON.parse erased the earlier invalid config occurrence");
    assert.equal(fixture.requests.length, 0);
  } finally { p.close(); }
});
