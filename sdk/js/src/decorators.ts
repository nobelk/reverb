import type { ReverbClient } from "./client.js";

export interface CachedCompletionOptions {
  /** Reverb cache namespace. Required. */
  namespace: string;
  /** Model identifier. Recommended for multi-model deployments. */
  modelId?: string;
  /** Optional TTL override (seconds) passed to `store`. */
  ttlSeconds?: number;
}

/**
 * Wrap an LLM-call function in a Reverb lookup-store flow.
 *
 * The wrapped function receives a single prompt string and returns a string
 * (sync return values are auto-promoted to `Promise<string>`). The decorator
 * does not parse OpenAI / Anthropic message arrays on the caller's behalf —
 * flattening multi-turn conversations into a cache key is a design choice
 * the application owns.
 *
 * ```ts
 * const ask = cachedCompletion(cache, { namespace: "support" })(
 *   async (prompt: string) => myLlm.complete(prompt),
 * );
 * ```
 */
export function cachedCompletion(
  cache: ReverbClient,
  opts: CachedCompletionOptions,
): <Fn extends (prompt: string) => string | Promise<string>>(fn: Fn) => (prompt: string) => Promise<string> {
  return (fn) => {
    return async (prompt: string): Promise<string> => {
      const hit = await cache.lookup({
        namespace: opts.namespace,
        prompt,
        modelId: opts.modelId,
      });
      if (hit.hit && hit.entry) {
        return hit.entry.response;
      }
      const response = await fn(prompt);
      await cache.store({
        namespace: opts.namespace,
        prompt,
        response,
        modelId: opts.modelId,
        ttlSeconds: opts.ttlSeconds,
      });
      return response;
    };
  };
}
