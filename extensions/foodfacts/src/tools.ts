import { ExtError, noContribution, unknownMethod, withExactParams, type ExactJSON, type Handler } from "@persona/extension";
import { barcode, clip, queryText, ReadError, type Product } from "./model.ts";
import { errorText, fmt, textsFor } from "./i18n.ts";
import { budget, type FoodFacts } from "./service.ts";
export function toolsFor(locale = "en") {
  const t = textsFor(locale);
  return [
    { name: "foodfacts_search", description: t.toolSearch, input_schema: { type: "object", required: ["query"], properties: { query: { type: "string", description: t.query }, limit: { type: "integer", minimum: 1, maximum: 20, description: t.limit } } } },
    { name: "foodfacts_product", description: t.toolProduct, input_schema: { type: "object", required: ["code"], properties: { code: { type: "string", description: t.code } } } },
  ];
}
export const tools = toolsFor();
function line(product: Product, index: number, locale: string): string {
  const t = textsFor(locale);
  return `${index + 1}. ${clip(product.name, 300)}\n   ${[product.brands, product.quantity, product.nutriscore ? fmt(t.nutriscore, { grade: product.nutriscore.toUpperCase() }) : "", product.nova ? fmt(t.nova, { group: product.nova }) : ""].filter(Boolean).join(" · ")}\n   ${t.ingredients}: ${product.ingredients || t.unknown}\n   ${t.allergens}: ${product.allergens || t.unknown}\n   ${product.url}`;
}
function argsFrom(raw: unknown, exact: ExactJSON | undefined, stringFields: readonly string[], allowLimit: boolean): Record<string, unknown> {
  if (exact) {
    if (exact.raw === "null") return {};
    if (!exact.raw.startsWith("{")) throw new ReadError("invalid");
    const args: Record<string, unknown> = {};
    for (const field of stringFields) for (const token of exact.fieldValues(field)) {
      if (token.raw === "null") continue;
      if (!token.raw.startsWith('"')) throw new ReadError("invalid");
      const value: unknown = JSON.parse(token.raw); if (typeof value !== "string") throw new ReadError("invalid"); args[field] = value;
    }
    if (allowLimit) {
      let hasLimit = false;
      let limit = 0n;
      for (const token of exact.fieldValues("limit")) {
        if (token.raw === "null") continue;
        try { limit = token.int64(limit); } catch { throw new ReadError("invalid"); }
        hasLimit = true;
      }
      if (hasLimit) args.limit = limit;
    }
    return args;
  }
  if (raw == null) return {};
  if (typeof raw !== "object" || Array.isArray(raw)) throw new ReadError("invalid");
  const value = raw as Record<string, unknown>;
  for (const field of stringFields) if (value[field] != null && typeof value[field] !== "string") throw new ReadError("invalid");
  if (allowLimit && value.limit != null && (typeof value.limit !== "number" || !Number.isSafeInteger(value.limit))) throw new ReadError("invalid");
  return value;
}
export async function callTool(service: FoodFacts, name: string, raw: unknown = {}, signal?: AbortSignal, exact?: ExactJSON): Promise<string> {
  if (name !== "foodfacts_search" && name !== "foodfacts_product") throw unknownMethod(`connector.tools.${name}`);
  const args = argsFrom(raw, exact, name === "foodfacts_search" ? ["query"] : ["code"], name === "foodfacts_search");
  const t = textsFor(service.locale), request = budget(signal, service.limits.tool);
  try {
    if (name === "foodfacts_search") {
      const query = queryText(args.query);
      let limit = 10;
      if (args.limit != null) {
        if (typeof args.limit === "bigint") {
          if (args.limit < 1n || args.limit > 20n) throw new ReadError("invalid");
          limit = Number(args.limit);
        } else {
          if (typeof args.limit !== "number" || !Number.isSafeInteger(args.limit) || args.limit < 1 || args.limit > 20) throw new ReadError("invalid");
          limit = args.limit;
        }
      }
      const result = await service.search(query, limit, false, request.signal);
      return clip((result.products.length ? result.products.map((product, index) => line(product, index, service.locale)).join("\n").trim() : fmt(t.results, { query }) + "\n" + t.empty) + `\n${t.attribution}${result.stale ? `\n${errorText(result.stale, service.locale)}` : ""}`, 29_999);
    }
    const result = await service.productResult(barcode(args.code), false, request.signal);
    return clip(line(result.product, 0, service.locale) + `\n${t.attribution}${result.stale ? `\n${errorText(result.stale, service.locale)}` : ""}`, 29_999);
  } catch (error) {
    if (!(error instanceof ReadError) && !signal?.aborted && request.signal.aborted && request.signal.reason instanceof ReadError) throw request.signal.reason;
    throw error;
  } finally { request.dispose(); }
}
export function connector(getService: () => FoodFacts): Handler { return withExactParams(async (method, params: unknown, call) => {
  const service = getService();
  if (params != null && (typeof params !== "object" || Array.isArray(params))) throw new ExtError(errorText(new ReadError("invalid"), service.locale), "invalid");
  const p = (params ?? {}) as Record<string, unknown>;
  if (p.contribution != null && typeof p.contribution !== "string") throw new ExtError(errorText(new ReadError("invalid"), service.locale), "invalid");
  if (p.contribution && p.contribution !== "foodfacts") throw noContribution(String(p.contribution));
  if (method === "status") return service.status();
  if (method === "tools.list") return { tools: toolsFor(service.locale) };
  if (method === "tools.call") {
    if (typeof p.name !== "string") throw new ExtError(errorText(new ReadError("invalid"), service.locale), "invalid");
    try { return { text: await callTool(service, p.name, p.args, undefined, call?.params?.field("args")) }; }
    catch (error) { if (error instanceof ReadError) throw new ExtError(errorText(error, service.locale), error.code === "invalid" ? "invalid" : undefined); throw error; }
  }
  throw unknownMethod("connector." + method);
}); }
