import {
  ReverbBadRequest,
  ReverbError,
  ReverbInternalError,
  ReverbNotFound,
  ReverbRateLimited,
  ReverbUnauthorized,
} from "./errors.js";
import type {
  CacheEntry,
  LookupResponse,
  SourceRef,
  StatsResponse,
  StoreResponse,
} from "./types.js";

export type FetchLike = (
  input: RequestInfo | URL,
  init?: RequestInit,
) => Promise<Response>;

export interface ReverbClientOptions {
  /** Reverb HTTP base URL. Default: `http://localhost:8080`. */
  baseUrl?: string;
  /** Bearer token for the `Authorization` header, when the server has auth enabled. */
  apiKey?: string;
  /** Override `fetch`. Useful on edge runtimes that pre-wrap globalThis.fetch. */
  fetch?: FetchLike;
  /** Default request timeout in milliseconds. Default: 30_000. */
  timeoutMs?: number;
}

export interface LookupParams {
  namespace: string;
  prompt: string;
  modelId?: string;
}

export interface StoreParams {
  namespace: string;
  prompt: string;
  response: string;
  modelId?: string;
  responseMeta?: Record<string, string>;
  sources?: SourceRef[];
  ttlSeconds?: number;
}

const DEFAULT_BASE_URL = "http://localhost:8080";
const DEFAULT_TIMEOUT_MS = 30_000;

function defaultFetch(): FetchLike {
  if (typeof globalThis.fetch !== "function") {
    throw new Error(
      "@reverb/client: global fetch is not available. " +
        "Use Node ≥ 18, a modern browser, an edge runtime, or pass fetch: ... to the constructor.",
    );
  }
  return globalThis.fetch.bind(globalThis);
}

async function raiseForStatus(resp: Response): Promise<void> {
  if (resp.status < 400) return;

  let payload: Record<string, unknown> = {};
  let message: string;
  try {
    payload = (await resp.clone().json()) as Record<string, unknown>;
    const errorField = payload["error"];
    message = typeof errorField === "string" ? errorField : `HTTP ${resp.status}`;
  } catch {
    try {
      message = await resp.text();
    } catch {
      message = `HTTP ${resp.status}`;
    }
  }

  switch (resp.status) {
    case 400:
      throw new ReverbBadRequest(message, payload);
    case 401:
      throw new ReverbUnauthorized(message, payload);
    case 404:
      throw new ReverbNotFound(message, payload);
    case 429: {
      const retryAfterRaw = resp.headers.get("Retry-After");
      const retryAfter =
        retryAfterRaw !== null && /^\d+$/.test(retryAfterRaw)
          ? Number.parseInt(retryAfterRaw, 10)
          : undefined;
      throw new ReverbRateLimited(message, retryAfter, payload);
    }
    default:
      if (resp.status >= 500 && resp.status < 600) {
        throw new ReverbInternalError(message, resp.status, payload);
      }
      throw new ReverbError(message, resp.status, payload);
  }
}

function withTimeout(timeoutMs: number, signal?: AbortSignal): {
  signal: AbortSignal;
  cancel: () => void;
} {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  const onUpstreamAbort = () => controller.abort();
  if (signal) {
    if (signal.aborted) controller.abort();
    else signal.addEventListener("abort", onUpstreamAbort, { once: true });
  }
  return {
    signal: controller.signal,
    cancel: () => {
      clearTimeout(timer);
      if (signal) signal.removeEventListener("abort", onUpstreamAbort);
    },
  };
}

export class ReverbClient {
  readonly baseUrl: string;
  private readonly apiKey?: string;
  private readonly fetchImpl: FetchLike;
  private readonly timeoutMs: number;

  constructor(options: ReverbClientOptions = {}) {
    this.baseUrl = (options.baseUrl ?? DEFAULT_BASE_URL).replace(/\/+$/, "");
    this.apiKey = options.apiKey;
    this.fetchImpl = options.fetch ?? defaultFetch();
    this.timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  }

  private headers(extra?: Record<string, string>): Record<string, string> {
    const headers: Record<string, string> = {
      Accept: "application/json",
      ...extra,
    };
    if (this.apiKey) headers["Authorization"] = `Bearer ${this.apiKey}`;
    return headers;
  }

  private async request(
    path: string,
    init: RequestInit & { json?: unknown } = {},
  ): Promise<Response> {
    const { signal, cancel } = withTimeout(this.timeoutMs, init.signal ?? undefined);
    const headers = this.headers(init.headers as Record<string, string> | undefined);
    let body: BodyInit | null | undefined = init.body;
    if (init.json !== undefined) {
      body = JSON.stringify(init.json);
      headers["Content-Type"] = "application/json";
    }
    try {
      const resp = await this.fetchImpl(`${this.baseUrl}${path}`, {
        ...init,
        headers,
        body,
        signal,
      });
      await raiseForStatus(resp);
      return resp;
    } finally {
      cancel();
    }
  }

  async lookup(params: LookupParams): Promise<LookupResponse> {
    const resp = await this.request("/v1/lookup", {
      method: "POST",
      json: {
        namespace: params.namespace,
        prompt: params.prompt,
        ...(params.modelId !== undefined ? { model_id: params.modelId } : {}),
      },
    });
    return (await resp.json()) as LookupResponse;
  }

  async store(params: StoreParams): Promise<StoreResponse> {
    const body: Record<string, unknown> = {
      namespace: params.namespace,
      prompt: params.prompt,
      response: params.response,
    };
    if (params.modelId !== undefined) body["model_id"] = params.modelId;
    if (params.responseMeta) body["response_meta"] = params.responseMeta;
    if (params.sources) body["sources"] = params.sources;
    if (params.ttlSeconds !== undefined) body["ttl_seconds"] = params.ttlSeconds;

    const resp = await this.request("/v1/store", { method: "POST", json: body });
    return (await resp.json()) as StoreResponse;
  }

  async invalidate(params: { sourceId: string }): Promise<number> {
    const resp = await this.request("/v1/invalidate", {
      method: "POST",
      json: { source_id: params.sourceId },
    });
    const data = (await resp.json()) as { invalidated_count: number };
    return data.invalidated_count;
  }

  async deleteEntry(entryId: string): Promise<void> {
    await this.request(`/v1/entries/${encodeURIComponent(entryId)}`, {
      method: "DELETE",
    });
  }

  async stats(): Promise<StatsResponse> {
    const resp = await this.request("/v1/stats", { method: "GET" });
    return (await resp.json()) as StatsResponse;
  }

  async healthz(): Promise<boolean> {
    try {
      const resp = await this.fetchImpl(`${this.baseUrl}/healthz`);
      return resp.status === 200;
    } catch {
      return false;
    }
  }
}

export type { CacheEntry };
