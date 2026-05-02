"""End-to-end smoke test for the reverb Python SDK.

Assumes a Reverb server is reachable at $REVERB_URL (default
``http://localhost:8080``). Exercises the lookup → store → lookup → invalidate
round trip and exits non-zero on any failure.

This is what the sdk-python CI job runs against a freshly-built ``cmd/reverb``
container. It is also the template users should run locally after
``pip install -e .``.
"""

from __future__ import annotations

import hashlib
import os
import sys
import uuid

from reverb import Reverb, SourceRef


def main() -> int:
    base_url = os.environ.get("REVERB_URL", "http://localhost:8080")
    namespace = f"smoke-{uuid.uuid4().hex[:8]}"
    prompt = "What is the capital of France?"
    response = "Paris."
    source_id = f"doc-{uuid.uuid4().hex[:8]}"
    content_hash = hashlib.sha256(b"Paris is the capital of France.").hexdigest()

    print(f"[smoke] base_url={base_url} namespace={namespace}")

    with Reverb(base_url=base_url) as cache:
        if not cache.healthz():
            print("[smoke] FAIL: server is not healthy", file=sys.stderr)
            return 1

        miss = cache.lookup(namespace=namespace, prompt=prompt)
        if miss.hit:
            print("[smoke] FAIL: expected cold miss, got hit", file=sys.stderr)
            return 1
        print("[smoke] cold lookup → miss (expected)")

        stored = cache.store(
            namespace=namespace,
            prompt=prompt,
            response=response,
            model_id="smoke-model",
            sources=[SourceRef(source_id=source_id, content_hash=content_hash)],
        )
        print(f"[smoke] stored entry id={stored.id}")

        warm = cache.lookup(namespace=namespace, prompt=prompt)
        if not warm.hit:
            print("[smoke] FAIL: expected exact hit after store, got miss", file=sys.stderr)
            return 1
        if warm.entry is None or warm.entry.response != response:
            print("[smoke] FAIL: hit returned wrong response", file=sys.stderr)
            return 1
        print(f"[smoke] warm lookup → tier={warm.tier} response={warm.entry.response!r}")

        count = cache.invalidate(source_id=source_id)
        if count < 1:
            print(f"[smoke] FAIL: invalidate returned {count}, expected ≥ 1", file=sys.stderr)
            return 1
        print(f"[smoke] invalidate({source_id}) removed {count} entries")

        post = cache.lookup(namespace=namespace, prompt=prompt)
        if post.hit:
            print("[smoke] FAIL: entry survived invalidation", file=sys.stderr)
            return 1
        print("[smoke] post-invalidate lookup → miss (expected)")

    print("[smoke] OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
