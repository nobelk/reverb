# OpenAI-compatible reverse-proxy mode

`cmd/reverb` can act as a transparent semantic cache in front of any
OpenAI-API-shaped upstream — OpenAI itself, vLLM, llama.cpp, Together,
Anyscale, Ollama (`/v1/`), or OpenRouter. Point any OpenAI SDK at the
Reverb listen address instead of `https://api.openai.com` and identical
requests after the first hit Reverb's cache.

## Run it

```bash
# Build the binary.
go build -o bin/reverb ./cmd/reverb

# Start the proxy. The --upstream flag is required.
bin/reverb --proxy openai --upstream https://api.openai.com --http-addr :8080
```

The proxy surfaces:

- `POST /v1/chat/completions` — cached. On hit: returns the stored body
  (or replays SSE chunks when `"stream": true`). On miss: forwards to
  `<upstream>/v1/chat/completions`, stores, and returns the response.
- `POST /v1/embeddings` — pass-through (out of scope for the first cut).
- `GET  /healthz` — always 200.

## Try it with curl

```bash
# First call: MISS (forwarded upstream, then cached).
curl -s -X POST http://localhost:8080/v1/chat/completions \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer $OPENAI_API_KEY" \
     -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"What is the capital of France?"}]}' \
     -D -

# Second call (identical body): HIT. Watch the X-Reverb-Cache header.
curl -s -X POST http://localhost:8080/v1/chat/completions \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer $OPENAI_API_KEY" \
     -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"What is the capital of France?"}]}' \
     -D -
```

The first response carries `X-Reverb-Cache: MISS`; the second carries
`X-Reverb-Cache: HIT`. The body is byte-identical because Reverb stores and
replays the upstream's response verbatim.

## Use it from any OpenAI SDK

Set the SDK's `base_url` to the proxy:

```python
# openai-python
from openai import OpenAI
client = OpenAI(base_url="http://localhost:8080/v1", api_key="...")
```

```ts
// openai-node
import OpenAI from "openai";
const client = new OpenAI({ baseURL: "http://localhost:8080/v1", apiKey: "..." });
```

The SDK doesn't know it's talking to a cache; the second identical request
just comes back faster.

## Cache-Control directives

Per RFC 9111, two directives are honored on the request:

- `Cache-Control: no-cache` — bypass the cache for this request (forces a
  miss; the response is still stored).
- `Cache-Control: no-store` — forward and return, but do not write the
  response to the cache.

```bash
# Force-refresh: skip the cache for the read.
curl -X POST ... -H 'Cache-Control: no-cache' ...
```

## Auth pass-through

The proxy forwards your `Authorization: Bearer ...` header to the upstream
verbatim. The upstream key is your key — the proxy does not hold one of its
own. This is intentional: in proxy mode Reverb is a cache, not a gateway.

## Streaming

`"stream": true` works end-to-end:

- On a hit, Reverb replays the cached chunks as SSE in the OpenAI shape.
- On a miss, Reverb tees the upstream's SSE stream to the caller while
  accumulating chunks for the cache write that follows.

## Caveats

- Proxy mode is mutually exclusive with Reverb's native `/v1/lookup` etc.
  surface in the same binary. Run two `cmd/reverb` instances side-by-side
  if you need both.
- The cache key includes the full request body (model + messages + tools +
  any sampling parameters). Two requests that differ only in `temperature`
  do not share a cache entry; that is by design.
- Embeddings are not cached in this mode (out of scope for the first cut).
