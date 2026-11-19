"""Structured JSON logging + request-id propagation, shared by every
vigil inference service (text, image, ...).

Named log_config.py (not logging.py) so it doesn't shadow the stdlib
`logging` module it imports from.

Design: a request-id ContextVar is set by RequestIDMiddleware for the
lifetime of a request and read by JsonFormatter on every log record emitted
during that request — so any log line written deep in application code
carries the right request_id without threading it through every call site.

This module knows nothing about any one service's Settings — the LOG_LEVEL
knob (if a service exposes one) is read by that service's own config and
passed into configure_logging(); this module just applies whatever level
it's given, defaulting to INFO to match every service's original behavior.
"""

import json
import logging
import sys
import time
import uuid
from collections.abc import Awaitable, Callable
from contextvars import ContextVar
from datetime import UTC, datetime

from starlette.middleware.base import BaseHTTPMiddleware
from starlette.requests import Request
from starlette.responses import Response

request_id_var: ContextVar[str | None] = ContextVar("request_id", default=None)

_EXTRA_FIELDS = ("method", "path", "status", "duration_ms")

access_logger = logging.getLogger("app.access")


class JsonFormatter(logging.Formatter):
    """Renders one JSON object per line: timestamp, level, logger, message,
    request_id, plus whichever of _EXTRA_FIELDS the record carries.
    """

    def format(self, record: logging.LogRecord) -> str:
        payload: dict[str, object] = {
            "timestamp": datetime.fromtimestamp(record.created, tz=UTC).isoformat(),
            "level": record.levelname,
            "logger": record.name,
            "message": record.getMessage(),
            "request_id": request_id_var.get(),
        }
        for field in _EXTRA_FIELDS:
            if hasattr(record, field):
                payload[field] = getattr(record, field)
        if record.exc_info:
            payload["exc_info"] = self.formatException(record.exc_info)
        return json.dumps(payload)


def configure_logging(level: int | str = logging.INFO) -> None:
    """Point the root logger at a single JSON stdout handler, and silence
    uvicorn's own plain-text access logger so pristine JSON logs aren't
    interleaved with a second, differently-formatted line per request —
    RequestIDMiddleware already emits one access-style JSON line per request.

    `level` defaults to INFO (every service's original behavior); a service
    that wants a LOG_LEVEL env knob reads it via its own Settings and passes
    the result here — this function stays agnostic of where the value came
    from.
    """
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JsonFormatter())

    root = logging.getLogger()
    root.handlers = [handler]
    root.setLevel(level)

    # Let uvicorn's own startup/error messages flow through our JSON handler
    # instead of uvicorn's default colored text formatter.
    for name in ("uvicorn", "uvicorn.error"):
        uv_logger = logging.getLogger(name)
        uv_logger.handlers = []
        uv_logger.propagate = True

    # Access logging is ours (one line per request via the middleware below);
    # disable uvicorn's built-in access logger to avoid a duplicate,
    # non-JSON line per request.
    uvicorn_access = logging.getLogger("uvicorn.access")
    uvicorn_access.handlers = []
    uvicorn_access.propagate = False


class RequestIDMiddleware(BaseHTTPMiddleware):
    """Reads X-Request-ID from the incoming request (generates a uuid4 if
    absent), makes it available to log records for the request's duration via
    a ContextVar, echoes it in the response header, and logs one access-style
    line (method, path, status, duration_ms) per request.
    """

    async def dispatch(
        self, request: Request, call_next: Callable[[Request], Awaitable[Response]]
    ) -> Response:
        request_id = request.headers.get("X-Request-ID") or str(uuid.uuid4())
        token = request_id_var.set(request_id)
        start = time.perf_counter()
        status_code = 500
        try:
            response = await call_next(request)
            status_code = response.status_code
            response.headers["X-Request-ID"] = request_id
            return response
        finally:
            duration_ms = round((time.perf_counter() - start) * 1000, 2)
            access_logger.info(
                "request handled",
                extra={
                    "method": request.method,
                    "path": request.url.path,
                    "status": status_code,
                    "duration_ms": duration_ms,
                },
            )
            request_id_var.reset(token)
