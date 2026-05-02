"""Exception hierarchy for Reverb.

Each HTTP error response in ``openapi/v1.yaml`` maps to a dedicated subclass
so callers can branch on type instead of inspecting status codes.
"""

from __future__ import annotations

from typing import Any


class ReverbError(Exception):
    """Base class for every error raised by the Reverb SDK."""

    def __init__(self, message: str, *, status_code: int | None = None,
                 payload: dict[str, Any] | None = None) -> None:
        super().__init__(message)
        self.status_code = status_code
        self.payload = payload or {}


class ReverbBadRequest(ReverbError):
    """HTTP 400 — request was malformed (invalid JSON, missing field, bad hash)."""


class ReverbUnauthorized(ReverbError):
    """HTTP 401 — bearer token is missing or invalid."""


class ReverbNotFound(ReverbError):
    """HTTP 404 — entry does not exist or is not visible to the caller."""


class ReverbRateLimited(ReverbError):
    """HTTP 429 — per-tenant rate limit exceeded.

    ``retry_after_seconds`` carries the parsed ``Retry-After`` header value so
    callers can implement backoff without re-parsing headers themselves.
    """

    def __init__(self, message: str, *, retry_after_seconds: int | None = None,
                 status_code: int | None = None,
                 payload: dict[str, Any] | None = None) -> None:
        super().__init__(message, status_code=status_code, payload=payload)
        self.retry_after_seconds = retry_after_seconds


class ReverbInternalError(ReverbError):
    """HTTP 5xx — unhandled server error."""
