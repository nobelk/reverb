// End-to-end smoke test for @reverb/client (Node runtime).
//
// Runs lookup → store → lookup → invalidate against a server at
// $REVERB_URL (default http://localhost:8080). Exits non-zero on failure.
//
// Run: `npm run build && npm run smoke:node`.

import crypto from "node:crypto";
import { ReverbClient } from "../dist/index.js";

const baseUrl = process.env.REVERB_URL ?? "http://localhost:8080";
const namespace = `smoke-${crypto.randomBytes(4).toString("hex")}`;
const sourceId = `doc-${crypto.randomBytes(4).toString("hex")}`;
const contentHash = crypto
  .createHash("sha256")
  .update("Paris is the capital of France.")
  .digest("hex");
const prompt = "What is the capital of France?";
const response = "Paris.";

console.log(`[smoke] base_url=${baseUrl} namespace=${namespace}`);

const cache = new ReverbClient({ baseUrl });

if (!(await cache.healthz())) {
  console.error("[smoke] FAIL: server is not healthy");
  process.exit(1);
}

const miss = await cache.lookup({ namespace, prompt });
if (miss.hit) {
  console.error("[smoke] FAIL: expected cold miss, got hit");
  process.exit(1);
}
console.log("[smoke] cold lookup → miss (expected)");

const stored = await cache.store({
  namespace,
  prompt,
  response,
  modelId: "smoke-model",
  sources: [{ source_id: sourceId, content_hash: contentHash }],
});
console.log(`[smoke] stored entry id=${stored.id}`);

const warm = await cache.lookup({ namespace, prompt });
if (!warm.hit || warm.entry?.response !== response) {
  console.error("[smoke] FAIL: expected exact hit after store");
  process.exit(1);
}
console.log(`[smoke] warm lookup → tier=${warm.tier} response=${warm.entry?.response}`);

const removed = await cache.invalidate({ sourceId });
if (removed < 1) {
  console.error(`[smoke] FAIL: invalidate returned ${removed}, expected ≥ 1`);
  process.exit(1);
}
console.log(`[smoke] invalidate(${sourceId}) removed ${removed} entries`);

const post = await cache.lookup({ namespace, prompt });
if (post.hit) {
  console.error("[smoke] FAIL: entry survived invalidation");
  process.exit(1);
}
console.log("[smoke] post-invalidate lookup → miss (expected)");

console.log("[smoke] OK");
