// Error hierarchy. Each HTTP error in openapi/v1.yaml maps to a dedicated
// subclass so callers can branch on `instanceof` instead of inspecting status
// codes.

export class ReverbError extends Error {
  readonly statusCode?: number;
  readonly payload?: Record<string, unknown>;

  constructor(message: string, statusCode?: number, payload?: Record<string, unknown>) {
    super(message);
    this.name = "ReverbError";
    this.statusCode = statusCode;
    this.payload = payload;
  }
}

export class ReverbBadRequest extends ReverbError {
  constructor(message: string, payload?: Record<string, unknown>) {
    super(message, 400, payload);
    this.name = "ReverbBadRequest";
  }
}

export class ReverbUnauthorized extends ReverbError {
  constructor(message: string, payload?: Record<string, unknown>) {
    super(message, 401, payload);
    this.name = "ReverbUnauthorized";
  }
}

export class ReverbNotFound extends ReverbError {
  constructor(message: string, payload?: Record<string, unknown>) {
    super(message, 404, payload);
    this.name = "ReverbNotFound";
  }
}

export class ReverbRateLimited extends ReverbError {
  /** Parsed value of the `Retry-After` header, in seconds. */
  readonly retryAfterSeconds?: number;

  constructor(
    message: string,
    retryAfterSeconds?: number,
    payload?: Record<string, unknown>,
  ) {
    super(message, 429, payload);
    this.name = "ReverbRateLimited";
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

export class ReverbInternalError extends ReverbError {
  constructor(message: string, statusCode: number, payload?: Record<string, unknown>) {
    super(message, statusCode, payload);
    this.name = "ReverbInternalError";
  }
}
