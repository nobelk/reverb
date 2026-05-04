# openai-chat — paraphrase-aware caching with a real embedder

This example puts Reverb in front of an LLM call with a **real** embedding
provider (OpenAI or Ollama) so you can see the value proposition that
matters: a paraphrased prompt is served from cache without an LLM round-
trip.

The other in-tree examples (`examples/basic`, `examples/semantic-cache`,
`examples/stale-knowledge`) use the `fake` embedder, which produces hash-
based vectors. Hashes don't preserve semantic similarity, so paraphrases
look unrelated. With a real embedder, "How do I reset my password?" and
"What's the procedure to change my password?" land close in vector space
and reverb's semantic tier finds the cached answer.

## What it does

Three lookup rounds against the same in-process cache:

1. **Cold cache** — the original prompt → miss. The example "generates" a
   canned response and stores it.
2. **Same prompt** — exact-tier hit, similarity = 1.0.
3. **Paraphrased prompt** — semantic-tier hit at similarity ≥ threshold,
   prints the source lineage that pinned the entry.

If round 3 misses, the program exits non-zero. CI uses that to gate the
merge — a regression in the embedder integration is treated as a release
blocker.

## Run it

### Against OpenAI

```sh
export OPENAI_API_KEY=sk-…
cd examples/openai-chat
go run . --provider=openai
```

### Against a local Ollama (offline)

Start Ollama and pull a small embedding model first:

```sh
ollama serve &
ollama pull nomic-embed-text
```

Then:

```sh
cd examples/openai-chat
go run . --provider=ollama
```

`OLLAMA_HOST` overrides the daemon URL (default `http://localhost:11434`);
`OLLAMA_EMBED_MODEL` overrides the model (default `nomic-embed-text`).

## Expected output

```
=== Reverb demo (provider=openai (text-embedding-3-small), threshold=0.85) ===

--- Round 1: cold cache, exact prompt → miss ---
  prompt: "How do I reset my password on the support portal?"
  result: tier=miss
  → stored; next round should be an exact hit

--- Round 2: identical prompt → exact hit ---
  prompt: "How do I reset my password on the support portal?"
  result: tier=exact similarity=1.0000

--- Round 3: paraphrased prompt → semantic hit ---
  prompt: "What's the procedure to change my support portal password?"
  result: tier=semantic similarity=0.91xx sources=[doc:password-reset]
  response: Visit the login page, click 'Forgot Password', …

=== Done — round 3's semantic hit is the demo's payoff ===
```

The exact similarity score in round 3 depends on the model. With
`text-embedding-3-small` the pair typically scores in the 0.88–0.94 range;
with `nomic-embed-text` (Ollama), 0.85–0.92. The default threshold is
`0.85` to give either provider headroom; pass `--threshold=0.90` to make
the gate stricter.

## Why round 3 is the interesting one

Anyone can make a key-value cache return the same answer for the same
question. Reverb's job is the question that *isn't* the same string but
*is* the same intent. Round 3 is that proof: a different prompt produces
the cached response because the embeddings are close enough, and the
cached entry remembers which source document fed it (via the lineage
`SourceRef`) so a downstream invalidation can sweep all paraphrases at
once.
