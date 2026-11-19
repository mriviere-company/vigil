"""JsonFormatter: every log line is a single JSON object with the required
base fields, plus the access-log extras when present on the record.

The logic under test now lives in vigil_common (see
inference/common/tests/test_log_config.py for the canonical unit tests) —
this file stays as an integration-level check that the text service is
still wired up to it correctly post-refactor.
"""

import json
import logging

from vigil_common.log_config import JsonFormatter, request_id_var


def _make_record(message: str = "hello", **extra: object) -> logging.LogRecord:
    record = logging.LogRecord(
        name="app.test",
        level=logging.INFO,
        pathname=__file__,
        lineno=1,
        msg=message,
        args=(),
        exc_info=None,
    )
    for key, value in extra.items():
        setattr(record, key, value)
    return record


def test_base_log_record_has_required_fields() -> None:
    formatter = JsonFormatter()
    payload = json.loads(formatter.format(_make_record("hello")))

    assert set(payload) == {"timestamp", "level", "logger", "message", "request_id"}
    assert payload["level"] == "INFO"
    assert payload["logger"] == "app.test"
    assert payload["message"] == "hello"


def test_request_id_from_contextvar_is_included() -> None:
    token = request_id_var.set("abc-123")
    try:
        formatter = JsonFormatter()
        payload = json.loads(formatter.format(_make_record("hello")))
    finally:
        request_id_var.reset(token)

    assert payload["request_id"] == "abc-123"


def test_access_log_extras_are_included_when_present() -> None:
    formatter = JsonFormatter()
    record = _make_record(
        "request handled", method="GET", path="/health", status=200, duration_ms=1.23
    )
    payload = json.loads(formatter.format(record))

    assert payload["method"] == "GET"
    assert payload["path"] == "/health"
    assert payload["status"] == 200
    assert payload["duration_ms"] == 1.23
