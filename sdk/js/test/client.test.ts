import { describe, expect, it, vi } from "vitest";
import {
  ReverbBadRequest,
  ReverbClient,
  ReverbRateLimited,
  cachedCompletion,
} from "../src/index.js";

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
    ...init,
  });
}

describe("ReverbClient", () => {
  it("returns a miss when the server reports hit=false", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ hit: false }));
    const cache = new ReverbClient({ fetch: fetchMock });
    const result = await cache.lookup({ namespace: "ns", prompt: "hello" });
    expect(result.hit).toBe(false);
    expect(result.entry).toBeUndefined();

    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe("http://localhost:8080/v1/lookup");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(init?.body as string)).toEqual({
      namespace: "ns",
      prompt: "hello",
    });
  });

  it("parses a semantic hit", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse({
        hit: true,
        tier: "semantic",
        similarity: 0.97,
        entry: {
          id: "e-1",
          created_at: "2026-04-30T00:00:00Z",
          namespace: "ns",
          prompt: "hello",
          model_id: "gpt-4o",
          response: "world",
          hit_count: 1,
        },
      }),
    );
    const cache = new ReverbClient({ fetch: fetchMock });
    const result = await cache.lookup({ namespace: "ns", prompt: "hello" });
    expect(result.hit).toBe(true);
    expect(result.tier).toBe("semantic");
    expect(result.entry?.response).toBe("world");
  });

  it("sends ttl_seconds and sources on store", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(
        { id: "e-2", created_at: "2026-04-30T00:00:00Z" },
        { status: 201 },
      ),
    );
    const cache = new ReverbClient({ fetch: fetchMock });
    await cache.store({
      namespace: "ns",
      prompt: "p",
      response: "r",
      modelId: "gpt-4o",
      sources: [{ source_id: "doc-1", content_hash: "a".repeat(64) }],
      ttlSeconds: 3600,
    });
    const body = JSON.parse(fetchMock.mock.calls[0]?.[1]?.body as string);
    expect(body).toMatchObject({
      namespace: "ns",
      prompt: "p",
      response: "r",
      model_id: "gpt-4o",
      ttl_seconds: 3600,
    });
    expect(body.sources).toHaveLength(1);
  });

  it("throws ReverbBadRequest on 400", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse({ error: "namespace is required" }, { status: 400 }),
    );
    const cache = new ReverbClient({ fetch: fetchMock });
    await expect(cache.lookup({ namespace: "", prompt: "hi" })).rejects.toBeInstanceOf(
      ReverbBadRequest,
    );
  });

  it("throws ReverbRateLimited with parsed Retry-After", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "slow down" }), {
        status: 429,
        headers: { "Content-Type": "application/json", "Retry-After": "12" },
      }),
    );
    const cache = new ReverbClient({ fetch: fetchMock });
    try {
      await cache.stats();
      throw new Error("expected throw");
    } catch (err) {
      expect(err).toBeInstanceOf(ReverbRateLimited);
      expect((err as ReverbRateLimited).retryAfterSeconds).toBe(12);
    }
  });

  it("attaches Authorization header when apiKey is set", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ hit: false }));
    const cache = new ReverbClient({ fetch: fetchMock, apiKey: "tok-abc" });
    await cache.lookup({ namespace: "ns", prompt: "hi" });
    const init = fetchMock.mock.calls[0]?.[1];
    const headers = init?.headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer tok-abc");
  });
});

describe("cachedCompletion", () => {
  it("calls upstream on miss, returns cached on hit", async () => {
    let lookupBody: { hit: boolean } | { hit: true; entry: unknown } = { hit: false };
    const fetchMock = vi.fn().mockImplementation(async (input: string) => {
      if (String(input).endsWith("/v1/lookup")) return jsonResponse(lookupBody);
      if (String(input).endsWith("/v1/store"))
        return jsonResponse({ id: "e-3", created_at: "2026-04-30T00:00:00Z" }, { status: 201 });
      return new Response("not found", { status: 404 });
    });

    const cache = new ReverbClient({ fetch: fetchMock });
    let upstream = 0;
    const ask = cachedCompletion(cache, { namespace: "ns", modelId: "m" })(
      async (prompt: string) => {
        upstream += 1;
        return `answer:${prompt}`;
      },
    );

    expect(await ask("hi")).toBe("answer:hi");
    expect(upstream).toBe(1);

    lookupBody = {
      hit: true,
      entry: {
        id: "e-3",
        created_at: "2026-04-30T00:00:00Z",
        namespace: "ns",
        prompt: "hi",
        model_id: "m",
        response: "answer:hi",
        hit_count: 1,
      },
    };
    expect(await ask("hi")).toBe("answer:hi");
    expect(upstream).toBe(1);
  });
});
