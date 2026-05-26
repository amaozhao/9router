// Typed error classes the router & admin can map to HTTP codes consistently.

export class AppError extends Error {
  constructor(message, { status = 500, code = 'internal_error', meta } = {}) {
    super(message);
    this.name = this.constructor.name;
    this.status = status;
    this.code = code;
    this.meta = meta;
  }
}

export class AuthError extends AppError {
  constructor(message = 'Unauthorized', meta) {
    super(message, { status: 401, code: 'unauthorized', meta });
  }
}

export class ForbiddenError extends AppError {
  constructor(message = 'Forbidden', meta) {
    super(message, { status: 403, code: 'forbidden', meta });
  }
}

export class NotFoundError extends AppError {
  constructor(message = 'Not found', meta) {
    super(message, { status: 404, code: 'not_found', meta });
  }
}

export class RateLimitError extends AppError {
  constructor(message = 'Rate limit exceeded', meta) {
    super(message, { status: 429, code: 'rate_limited', meta });
  }
}

export class UpstreamError extends AppError {
  constructor(message = 'Upstream error', meta) {
    super(message, { status: 502, code: 'upstream_error', meta });
  }
}

export class ValidationError extends AppError {
  constructor(message = 'Validation failed', meta) {
    super(message, { status: 400, code: 'validation_error', meta });
  }
}

export class NoAccountAvailableError extends AppError {
  constructor(meta) {
    super('No upstream account available (all in cooldown or none configured)',
      { status: 503, code: 'no_account_available', meta });
  }
}
