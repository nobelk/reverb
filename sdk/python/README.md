# reverb (Python SDK)

Python client for [Reverb](https://github.com/nobelk/reverb), a semantic
response cache for LLM applications.

> **Status:** `0.1.0rc0`. Build-ready, not yet published. PyPI release lands as
> part of Phase 1 of the adoption-surface roadmap.

## Install

```bash
pip install reverb            # core
pip install 'reverb[openai]'  # + @cached_completion for OpenAI
pip install 'reverb[anthropic]'
pip install 'reverb[all]'
```

## Quick start

```python
from reverb import Reverb

cache = Reverb(base_url="http://localhost:8080")

# 1) Try the cache.
hit = cache.lookup(namespace="support-bot", prompt="How do I reset my password?")
if hit.hit:
    print(hit.entry.response)
else:
    # 2) Call the upstream model yourself.
    response = call_my_llm("How do I reset my password?")
    # 3) Persist for next time.
    cache.store(
        namespace="support-bot",
        prompt="How do I reset my password?",
        response=response,
        model_id="gpt-4o",
    )
```

## `@cached_completion` — wrap your existing OpenAI / Anthropic calls

```python
from openai import OpenAI
from reverb import Reverb, cached_completion

cache = Reverb(base_url="http://localhost:8080")
client = OpenAI()

@cached_completion(cache, namespace="support-bot")
def ask(prompt: str) -> str:
    resp = client.chat.completions.create(
        model="gpt-4o",
        messages=[{"role": "user", "content": prompt}],
    )
    return resp.choices[0].message.content
```

The first call routes to OpenAI and stores the result; subsequent calls with
the same (or paraphrased) prompt return the cached response.

## Wire contract

This SDK speaks the HTTP REST surface defined in
[`openapi/v1.yaml`](../../openapi/v1.yaml). The contract is the source of
truth; this client is a hand-rolled wrapper around `httpx`.

A future minor release will replace the hand-rolled wire layer with one
generated from `openapi/v1.yaml` via `openapi-generator-cli`. The public
`reverb.Reverb` / `cached_completion` surface will not change. See
[`openapi-generator/config.yaml`](openapi-generator/config.yaml) for the
generator config and `make sdk-regen-python` (in the main repo Makefile) for
the regeneration command.

## Smoke test

```bash
make -C ../.. build                       # build the Go server
../../bin/reverb &                        # start it on :8080
python scripts/smoke_test.py              # round-trips lookup → store → lookup
```
