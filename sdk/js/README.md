# @reverb/client

TypeScript / JavaScript client for [Reverb](https://github.com/nobelk/reverb),
a semantic response cache for LLM applications.

> **Status:** `0.1.0-rc.0`. Build-ready, not yet published. npm release lands
> as part of Phase 1 of the adoption-surface roadmap.

Runs on:

- Node ≥ 18 (uses the global `fetch`)
- Cloudflare Workers
- Vercel Edge / other edge runtimes
- Modern browsers (with CORS configured on the server)

## Install

```bash
npm install @reverb/client
```

## Quick start

```ts
import { ReverbClient } from "@reverb/client";

const cache = new ReverbClient({ baseUrl: "http://localhost:8080" });

const hit = await cache.lookup({
  namespace: "support-bot",
  prompt: "How do I reset my password?",
});

if (hit.hit) {
  console.log(hit.entry?.response);
} else {
  const response = await callMyLlm("How do I reset my password?");
  await cache.store({
    namespace: "support-bot",
    prompt: "How do I reset my password?",
    response,
    modelId: "gpt-4o",
  });
}
```

## `cachedCompletion` — wrap an existing LLM-call function

```ts
import { ReverbClient, cachedCompletion } from "@reverb/client";
import OpenAI from "openai";

const cache = new ReverbClient({ baseUrl: "http://localhost:8080" });
const client = new OpenAI();

const ask = cachedCompletion(cache, {
  namespace: "support-bot",
  modelId: "gpt-4o",
})(async (prompt: string): Promise<string> => {
  const resp = await client.chat.completions.create({
    model: "gpt-4o",
    messages: [{ role: "user", content: prompt }],
  });
  return resp.choices[0]?.message.content ?? "";
});

const answer = await ask("How do I reset my password?");
```

## Wire contract

This SDK speaks the HTTP REST surface defined in
[`openapi/v1.yaml`](../../openapi/v1.yaml). The contract is the source of
truth; this client is a hand-rolled wrapper around the global `fetch`.

A future minor release will replace the hand-rolled wire layer with one
generated from `openapi/v1.yaml` via `openapi-generator-cli`. The exported
`ReverbClient` / `cachedCompletion` surface will not change. See
[`openapi-generator/config.yaml`](openapi-generator/config.yaml) for the
generator config and `make sdk-regen-js` (in the main repo Makefile) for
the regeneration command.

## Smoke tests

```bash
make -C ../.. build      # build the Go server
../../bin/reverb &       # start it on :8080

npm install
npm run build
npm run smoke:node       # round-trips lookup → store → lookup from Node
npm run smoke:workers    # same, but inside a miniflare-emulated Worker
```
