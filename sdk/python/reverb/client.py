"""Sync and async Reverb clients.

Speaks the REST surface defined in ``openapi/v1.yaml``. Each method maps 1:1
to one operation in the spec — ``lookup`` → ``POST /v1/lookup``, etc.
"""

from __future__ import annotations

from typing import Any, Iterable, Mapping

import httpx

from reverb._types import (
    CacheEntry,
    LookupResponse,
    SourceRef,
    StatsResponse,
    StoreResponse,
)
from reverb.errors import (
    ReverbBadRequest,
    ReverbError,
    ReverbInternalError,
    ReverbNotFound,
    ReverbRateLimited,
    ReverbUnauthorized,
)

DEFAULT_TIMEOUT_SECONDS = 30.0


def _raise_for_status(resp: httpx.Response) -> None:
    if resp.status_code < 400:
        return
    try:
        payload = resp.json()
        message = str(payload.get("error") or resp.text)
    except ValueError:
        payload = {}
        message = resp.text or f"HTTP {resp.status_code}"
    if resp.status_code == 400:
        raise ReverbBadRequest(message, status_code=400, payload=payload)
    if resp.status_code == 401:
        raise ReverbUnauthorized(message, status_code=401, payload=payload)
    if resp.status_code == 404:
        raise ReverbNotFound(message, status_code=404, payload=payload)
    if resp.status_code == 429:
        retry_after = resp.headers.get("Retry-After")
        retry_seconds: int | None
        try:
            retry_seconds = int(retry_after) if retry_after is not None else None
        except ValueError:
            retry_seconds = None
        raise ReverbRateLimited(
            message,
            retry_after_seconds=retry_seconds,
            status_code=429,
            payload=payload,
        )
    if 500 <= resp.status_code < 600:
        raise ReverbInternalError(message, status_code=resp.status_code, payload=payload)
    raise ReverbError(message, status_code=resp.status_code, payload=payload)


def _store_body(
    *,
    namespace: str,
    prompt: str,
    response: str,
    model_id: str | None,
    response_meta: Mapping[str, str] | None,
    sources: Iterable[SourceRef] | None,
    ttl_seconds: int | None,
) -> dict[str, Any]:
    body: dict[str, Any] = {
        "namespace": namespace,
        "prompt": prompt,
        "response": response,
    }
    if model_id is not None:
        body["model_id"] = model_id
    if response_meta:
        body["response_meta"] = dict(response_meta)
    if sources is not None:
        body["sources"] = [s.to_wire() for s in sources]
    if ttl_seconds is not None:
        body["ttl_seconds"] = int(ttl_seconds)
    return body


def _lookup_body(*, namespace: str, prompt: str, model_id: str | None) -> dict[str, Any]:
    body: dict[str, Any] = {"namespace": namespace, "prompt": prompt}
    if model_id is not None:
        body["model_id"] = model_id
    return body


def _bearer_headers(api_key: str | None) -> dict[str, str]:
    if api_key is None:
        return {}
    return {"Authorization": f"Bearer {api_key}"}


class Reverb:
    """Synchronous client for a Reverb HTTP server.

    Construction is cheap; under the hood we build a single ``httpx.Client``
    and reuse it for the lifetime of the instance. Use as a context manager
    or call :meth:`close` to release the connection pool.
    """

    def __init__(
        self,
        base_url: str = "http://localhost:8080",
        *,
        api_key: str | None = None,
        timeout: float = DEFAULT_TIMEOUT_SECONDS,
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._http = httpx.Client(
            base_url=self._base_url,
            headers=_bearer_headers(api_key),
            timeout=timeout,
            transport=transport,
        )

    def __enter__(self) -> Reverb:
        return self

    def __exit__(self, *_exc_info: object) -> None:
        self.close()

    def close(self) -> None:
        self._http.close()

    def lookup(
        self,
        *,
        namespace: str,
        prompt: str,
        model_id: str | None = None,
    ) -> LookupResponse:
        resp = self._http.post("/v1/lookup", json=_lookup_body(
            namespace=namespace, prompt=prompt, model_id=model_id,
        ))
        _raise_for_status(resp)
        return LookupResponse.from_wire(resp.json())

    def store(
        self,
        *,
        namespace: str,
        prompt: str,
        response: str,
        model_id: str | None = None,
        response_meta: Mapping[str, str] | None = None,
        sources: Iterable[SourceRef] | None = None,
        ttl_seconds: int | None = None,
    ) -> StoreResponse:
        resp = self._http.post("/v1/store", json=_store_body(
            namespace=namespace, prompt=prompt, response=response,
            model_id=model_id, response_meta=response_meta,
            sources=sources, ttl_seconds=ttl_seconds,
        ))
        _raise_for_status(resp)
        return StoreResponse.from_wire(resp.json())

    def invalidate(self, *, source_id: str) -> int:
        resp = self._http.post("/v1/invalidate", json={"source_id": source_id})
        _raise_for_status(resp)
        return int(resp.json()["invalidated_count"])

    def delete_entry(self, entry_id: str) -> None:
        resp = self._http.delete(f"/v1/entries/{entry_id}")
        _raise_for_status(resp)

    def stats(self) -> StatsResponse:
        resp = self._http.get("/v1/stats")
        _raise_for_status(resp)
        return StatsResponse.from_wire(resp.json())

    def healthz(self) -> bool:
        try:
            resp = self._http.get("/healthz")
            return resp.status_code == 200
        except httpx.HTTPError:
            return False


class AsyncReverb:
    """Async counterpart of :class:`Reverb`. Same surface, ``await``-able."""

    def __init__(
        self,
        base_url: str = "http://localhost:8080",
        *,
        api_key: str | None = None,
        timeout: float = DEFAULT_TIMEOUT_SECONDS,
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._http = httpx.AsyncClient(
            base_url=self._base_url,
            headers=_bearer_headers(api_key),
            timeout=timeout,
            transport=transport,
        )

    async def __aenter__(self) -> AsyncReverb:
        return self

    async def __aexit__(self, *_exc_info: object) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        await self._http.aclose()

    async def lookup(
        self,
        *,
        namespace: str,
        prompt: str,
        model_id: str | None = None,
    ) -> LookupResponse:
        resp = await self._http.post("/v1/lookup", json=_lookup_body(
            namespace=namespace, prompt=prompt, model_id=model_id,
        ))
        _raise_for_status(resp)
        return LookupResponse.from_wire(resp.json())

    async def store(
        self,
        *,
        namespace: str,
        prompt: str,
        response: str,
        model_id: str | None = None,
        response_meta: Mapping[str, str] | None = None,
        sources: Iterable[SourceRef] | None = None,
        ttl_seconds: int | None = None,
    ) -> StoreResponse:
        resp = await self._http.post("/v1/store", json=_store_body(
            namespace=namespace, prompt=prompt, response=response,
            model_id=model_id, response_meta=response_meta,
            sources=sources, ttl_seconds=ttl_seconds,
        ))
        _raise_for_status(resp)
        return StoreResponse.from_wire(resp.json())

    async def invalidate(self, *, source_id: str) -> int:
        resp = await self._http.post("/v1/invalidate", json={"source_id": source_id})
        _raise_for_status(resp)
        return int(resp.json()["invalidated_count"])

    async def delete_entry(self, entry_id: str) -> None:
        resp = await self._http.delete(f"/v1/entries/{entry_id}")
        _raise_for_status(resp)

    async def stats(self) -> StatsResponse:
        resp = await self._http.get("/v1/stats")
        _raise_for_status(resp)
        return StatsResponse.from_wire(resp.json())


__all__ = ["AsyncReverb", "Reverb"]
_ = CacheEntry
