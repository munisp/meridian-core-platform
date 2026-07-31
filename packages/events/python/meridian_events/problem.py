"""Uniform RFC7807 (application/problem+json) error handling for the
FastAPI services. Install once at app construction:

    from meridian_events.problem import install_problem_handlers
    app = FastAPI(...)
    install_problem_handlers(app)

Converts FastAPI/Starlette HTTPException and request-validation errors into
problem documents and guarantees the content-type is
application/problem+json.
"""
from __future__ import annotations

from typing import Any

from fastapi import FastAPI, HTTPException
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse

MEDIA_TYPE = "application/problem+json"


def problem_document(status: int, title: str, detail: Any = "",
                     type_: str = "about:blank") -> dict:
    if isinstance(detail, list):  # pydantic validation errors
        detail = "; ".join(
            f"{'.'.join(str(p) for p in e.get('loc', []))}: {e.get('msg', '')}"
            for e in detail
        )
    return {"type": type_, "title": title, "status": status, "detail": str(detail)}


def problem_response(status: int, title: str, detail: Any = "",
                     type_: str = "about:blank") -> JSONResponse:
    return JSONResponse(status_code=status, media_type=MEDIA_TYPE,
                        content=problem_document(status, title, detail, type_))


_TITLES = {
    400: "bad request", 401: "unauthorized", 403: "forbidden",
    404: "not found", 405: "method not allowed", 409: "conflict",
    422: "unprocessable entity", 429: "too many requests",
    500: "internal error", 502: "bad gateway", 503: "service unavailable",
}


def install_problem_handlers(app: FastAPI) -> None:
    @app.exception_handler(HTTPException)
    async def _http_exc(_request, exc: HTTPException):  # noqa: ANN001, ANN202
        title = _TITLES.get(exc.status_code, "error")
        detail = exc.detail if isinstance(exc.detail, str) else str(exc.detail)
        # FastAPI convention: detail holds the message; keep it as detail.
        return problem_response(exc.status_code, title, detail)

    @app.exception_handler(RequestValidationError)
    async def _val_exc(_request, exc: RequestValidationError):  # noqa: ANN001, ANN202
        return problem_response(422, "unprocessable entity", exc.errors())

    @app.exception_handler(Exception)
    async def _unhandled(_request, exc: Exception):  # noqa: ANN001, ANN202
        return problem_response(500, "internal error", f"{type(exc).__name__}")
