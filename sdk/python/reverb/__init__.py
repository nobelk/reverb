"""Reverb — Python client for the Reverb semantic response cache.

The public surface intentionally mirrors `openapi/v1.yaml`:

- :class:`Reverb` / :class:`AsyncReverb` — sync and async clients that speak
  the `/v1/*` REST endpoints.
- :func:`cached_completion` — decorator that wraps OpenAI / Anthropic-style
  callables in a Reverb lookup-then-store flow.

The wire shapes (:class:`LookupResponse`, :class:`CacheEntry`, …) are
re-exported so callers do not need to import from ``reverb._types``.
"""

from reverb._types import (
    CacheEntry,
    LookupResponse,
    SourceRef,
    StatsResponse,
    StoreResponse,
)
from reverb.client import AsyncReverb, Reverb
from reverb.decorators import cached_completion
from reverb.errors import (
    ReverbBadRequest,
    ReverbError,
    ReverbInternalError,
    ReverbNotFound,
    ReverbRateLimited,
    ReverbUnauthorized,
)

__all__ = [
    "AsyncReverb",
    "CacheEntry",
    "LookupResponse",
    "Reverb",
    "ReverbBadRequest",
    "ReverbError",
    "ReverbInternalError",
    "ReverbNotFound",
    "ReverbRateLimited",
    "ReverbUnauthorized",
    "SourceRef",
    "StatsResponse",
    "StoreResponse",
    "cached_completion",
]

__version__ = "0.1.0rc0"
