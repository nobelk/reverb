export { ReverbClient } from "./client.js";
export type {
  FetchLike,
  LookupParams,
  ReverbClientOptions,
  StoreParams,
} from "./client.js";

export { cachedCompletion } from "./decorators.js";
export type { CachedCompletionOptions } from "./decorators.js";

export {
  ReverbBadRequest,
  ReverbError,
  ReverbInternalError,
  ReverbNotFound,
  ReverbRateLimited,
  ReverbUnauthorized,
} from "./errors.js";

export type {
  CacheEntry,
  ErrorPayload,
  HealthResponse,
  InvalidateRequest,
  InvalidateResponse,
  LookupRequest,
  LookupResponse,
  SourceRef,
  StatsResponse,
  StoreRequest,
  StoreResponse,
} from "./types.js";

export const VERSION = "0.1.0-rc.0";
