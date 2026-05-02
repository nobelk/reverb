// Cloudflare Workers smoke test for @reverb/client.
//
// Boots a miniflare-emulated Worker, executes the same lookup → store →
// lookup round trip the Node smoke test runs, and exits non-zero on failure.
//
// The worker code under test is the *built* dist/index.js — we want to
// validate that the published bundle works in a Workers runtime, not just
// the TypeScript source.
//
// Run: `npm run build && npm run smoke:workers`.

import { Miniflare } from "miniflare";

const baseUrl = process.env.REVERB_URL ?? "http://localhost:8080";

const workerScript = `
import { ReverbClient } from "${new URL("../dist/index.js", import.meta.url).pathname}";

export default {
  async fetch(request, env) {
    const cache = new ReverbClient({ baseUrl: env.REVERB_URL });
    const namespace = "smoke-workers-" + crypto.randomUUID().slice(0, 8);
    const prompt = "Hello from a Worker";

    const miss = await cache.lookup({ namespace, prompt });
    if (miss.hit) return new Response("FAIL: expected miss", { status: 500 });

    const stored = await cache.store({
      namespace, prompt, response: "Hi back!", modelId: "smoke-model",
    });

    const warm = await cache.lookup({ namespace, prompt });
    if (!warm.hit || warm.entry?.response !== "Hi back!") {
      return new Response("FAIL: expected hit after store", { status: 500 });
    }
    return new Response(JSON.stringify({ ok: true, entry_id: stored.id }), {
      headers: { "Content-Type": "application/json" },
    });
  },
};
`;

const mf = new Miniflare({
  modules: true,
  script: workerScript,
  bindings: { REVERB_URL: baseUrl },
  compatibilityDate: "2024-08-01",
});

try {
  const resp = await mf.dispatchFetch("https://example.com/");
  const body = await resp.text();
  if (resp.status !== 200) {
    console.error(`[smoke:workers] FAIL: status=${resp.status} body=${body}`);
    process.exit(1);
  }
  console.log(`[smoke:workers] OK: ${body}`);
} finally {
  await mf.dispose();
}
