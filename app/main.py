from __future__ import annotations

from fastapi import FastAPI

from app.config import settings
from app.db import init_db


def build_app() -> FastAPI:
    app = FastAPI(title=settings.app_name)
    init_db()

    @app.get("/healthz")
    def healthz() -> dict[str, str]:
        return {"status": "ok", "service": settings.app_name}

    return app
