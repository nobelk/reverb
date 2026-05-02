"""Unit tests for the Reverb client. Uses respx to mock httpx without a server."""

from __future__ import annotations

import httpx
import pytest
import respx

from reverb import (
    Reverb,
    ReverbBadRequest,
    ReverbRateLimited,
    SourceRef,
    cached_completion,
)


@respx.mock
def test_lookup_miss() -> None:
    respx.post("http://localhost:8080/v1/lookup").mock(
        return_value=httpx.Response(200, json={"hit": False}),
    )
    with Reverb() as cache:
        result = cache.lookup(namespace="ns", prompt="hello")
    assert result.hit is False
    assert result.entry is None


@respx.mock
def test_lookup_semantic_hit() -> None:
    payload = {
        "hit": True,
        "tier": "semantic",
        "similarity": 0.97,
        "entry": {
            "id": "e-1",
            "created_at": "2026-04-30T00:00:00Z",
            "namespace": "ns",
            "prompt": "hello",
            "model_id": "gpt-4o",
            "response": "world",
            "hit_count": 3,
        },
    }
    respx.post("http://localhost:8080/v1/lookup").mock(
        return_value=httpx.Response(200, json=payload),
    )
    with Reverb() as cache:
        result = cache.lookup(namespace="ns", prompt="hello")
    assert result.hit is True
    assert result.tier == "semantic"
    assert result.similarity == pytest.approx(0.97)
    assert result.entry is not None
    assert result.entry.response == "world"


@respx.mock
def test_store_with_sources() -> None:
    route = respx.post("http://localhost:8080/v1/store").mock(
        return_value=httpx.Response(
            201, json={"id": "e-2", "created_at": "2026-04-30T00:00:00Z"},
        ),
    )
    with Reverb() as cache:
        result = cache.store(
            namespace="ns",
            prompt="hello",
            response="world",
            model_id="gpt-4o",
            sources=[SourceRef(source_id="doc-1", content_hash="a" * 64)],
            ttl_seconds=3600,
        )
    assert result.id == "e-2"
    sent = route.calls.last.request.read()
    assert b'"source_id":"doc-1"' in sent
    assert b'"ttl_seconds":3600' in sent


@respx.mock
def test_invalidate() -> None:
    respx.post("http://localhost:8080/v1/invalidate").mock(
        return_value=httpx.Response(200, json={"invalidated_count": 7}),
    )
    with Reverb() as cache:
        assert cache.invalidate(source_id="doc-1") == 7


@respx.mock
def test_bad_request_raises() -> None:
    respx.post("http://localhost:8080/v1/lookup").mock(
        return_value=httpx.Response(400, json={"error": "namespace is required"}),
    )
    with Reverb() as cache, pytest.raises(ReverbBadRequest) as excinfo:
        cache.lookup(namespace="", prompt="hello")
    assert "namespace is required" in str(excinfo.value)
    assert excinfo.value.status_code == 400


@respx.mock
def test_rate_limit_carries_retry_after() -> None:
    respx.get("http://localhost:8080/v1/stats").mock(
        return_value=httpx.Response(
            429, headers={"Retry-After": "12"}, json={"error": "slow down"},
        ),
    )
    with Reverb() as cache, pytest.raises(ReverbRateLimited) as excinfo:
        cache.stats()
    assert excinfo.value.retry_after_seconds == 12


@respx.mock
def test_cached_completion_miss_then_hit() -> None:
    lookup = respx.post("http://localhost:8080/v1/lookup")
    store = respx.post("http://localhost:8080/v1/store").mock(
        return_value=httpx.Response(
            201, json={"id": "e-3", "created_at": "2026-04-30T00:00:00Z"},
        ),
    )

    call_count = 0

    with Reverb() as cache:
        @cached_completion(cache, namespace="ns", model_id="gpt-4o")
        def ask(prompt: str) -> str:
            nonlocal call_count
            call_count += 1
            return f"answer to {prompt}"

        # First call: miss → upstream → store.
        lookup.mock(return_value=httpx.Response(200, json={"hit": False}))
        assert ask("hello") == "answer to hello"
        assert call_count == 1
        assert store.called

        # Second call: hit → no upstream call.
        hit_payload = {
            "hit": True,
            "tier": "exact",
            "entry": {
                "id": "e-3",
                "created_at": "2026-04-30T00:00:00Z",
                "namespace": "ns",
                "prompt": "hello",
                "model_id": "gpt-4o",
                "response": "answer to hello",
                "hit_count": 1,
            },
        }
        lookup.mock(return_value=httpx.Response(200, json=hit_payload))
        assert ask("hello") == "answer to hello"
        assert call_count == 1
