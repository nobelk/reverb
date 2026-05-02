"""``@cached_completion`` — wrap an LLM-call function in a Reverb lookup-store flow.

The decorator is intentionally small and explicit: the wrapped function takes
a prompt string (extracted from a named argument), and the decorator handles
the lookup → call → store sequence. It does **not** attempt to parse OpenAI
or Anthropic message arrays on the caller's behalf — flattening a multi-turn
conversation into a cache key is a design call that the application owns,
not the SDK.

Example::

    from openai import OpenAI
    from reverb import Reverb, cached_completion

    cache = Reverb()
    client = OpenAI()

    @cached_completion(cache, namespace="support-bot", model_id="gpt-4o")
    def ask(prompt: str) -> str:
        resp = client.chat.completions.create(
            model="gpt-4o",
            messages=[{"role": "user", "content": prompt}],
        )
        return resp.choices[0].message.content
"""

from __future__ import annotations

import functools
import inspect
from typing import Any, Awaitable, Callable, Union, cast

from reverb.client import AsyncReverb, Reverb

SyncFn = Callable[..., str]
AsyncFn = Callable[..., Awaitable[str]]
AnyFn = Union[SyncFn, AsyncFn]


def _extract_prompt(fn: Callable[..., Any], prompt_arg: str,
                    args: tuple[Any, ...], kwargs: dict[str, Any]) -> str:
    """Pull the prompt out of (args, kwargs) using the wrapped function's signature.

    Looks for ``prompt_arg`` first as a kwarg, then as a positional arg by
    consulting ``inspect.signature``. Raises ``TypeError`` if absent — the
    decorator is unusable on a function that doesn't expose a prompt string.
    """
    if prompt_arg in kwargs:
        value = kwargs[prompt_arg]
    else:
        sig = inspect.signature(fn)
        params = list(sig.parameters)
        if prompt_arg not in params:
            raise TypeError(
                f"@cached_completion: wrapped function has no argument named "
                f"'{prompt_arg}'. Pass prompt_arg=... to point at the right one.",
            )
        idx = params.index(prompt_arg)
        if idx >= len(args):
            raise TypeError(
                f"@cached_completion: argument '{prompt_arg}' was not supplied",
            )
        value = args[idx]
    if not isinstance(value, str):
        raise TypeError(
            f"@cached_completion: argument '{prompt_arg}' must be a str, "
            f"got {type(value).__name__}",
        )
    return value


def cached_completion(
    cache: Reverb | AsyncReverb,
    *,
    namespace: str,
    model_id: str | None = None,
    prompt_arg: str = "prompt",
    ttl_seconds: int | None = None,
) -> Callable[[AnyFn], AnyFn]:
    """Wrap an LLM-call function in a Reverb cache.

    Parameters
    ----------
    cache:
        A :class:`Reverb` for sync wrapped functions, or an
        :class:`AsyncReverb` for ``async def`` wrapped functions. The decorator
        validates this match and raises ``TypeError`` on mismatch.
    namespace:
        Reverb cache namespace. Required.
    model_id:
        Model identifier passed to ``store``. Recommended for multi-model
        deployments so ``scope_by_model`` filters correctly.
    prompt_arg:
        Name of the wrapped function's argument that carries the prompt
        string. Defaults to ``"prompt"``.
    ttl_seconds:
        Optional override for the entry TTL on store.
    """

    def decorate(fn: AnyFn) -> AnyFn:
        is_async = inspect.iscoroutinefunction(fn)
        if is_async and not isinstance(cache, AsyncReverb):
            raise TypeError(
                "@cached_completion: async function requires an AsyncReverb cache",
            )
        if not is_async and isinstance(cache, AsyncReverb):
            raise TypeError(
                "@cached_completion: sync function requires a Reverb cache",
            )

        if is_async:
            async_cache = cast(AsyncReverb, cache)
            async_fn = cast(AsyncFn, fn)

            @functools.wraps(async_fn)
            async def async_wrapper(*args: Any, **kwargs: Any) -> str:
                prompt = _extract_prompt(async_fn, prompt_arg, args, kwargs)
                hit = await async_cache.lookup(
                    namespace=namespace, prompt=prompt, model_id=model_id,
                )
                if hit.hit and hit.entry is not None:
                    return hit.entry.response
                response = await async_fn(*args, **kwargs)
                await async_cache.store(
                    namespace=namespace,
                    prompt=prompt,
                    response=response,
                    model_id=model_id,
                    ttl_seconds=ttl_seconds,
                )
                return response

            return cast(AnyFn, async_wrapper)

        sync_cache = cast(Reverb, cache)
        sync_fn = cast(SyncFn, fn)

        @functools.wraps(sync_fn)
        def sync_wrapper(*args: Any, **kwargs: Any) -> str:
            prompt = _extract_prompt(sync_fn, prompt_arg, args, kwargs)
            hit = sync_cache.lookup(
                namespace=namespace, prompt=prompt, model_id=model_id,
            )
            if hit.hit and hit.entry is not None:
                return hit.entry.response
            response = sync_fn(*args, **kwargs)
            sync_cache.store(
                namespace=namespace,
                prompt=prompt,
                response=response,
                model_id=model_id,
                ttl_seconds=ttl_seconds,
            )
            return response

        return cast(AnyFn, sync_wrapper)

    return decorate


__all__ = ["cached_completion"]
