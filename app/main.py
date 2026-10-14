from __future__ import annotations

from fastapi import FastAPI

from app.api import approvals, audit, tasks
from app.config import settings
from app.db import init_db


def build_app() -> FastAPI:
    app = FastAPI(title=settings.app_name)
    init_db()

    app.include_router(tasks.router)
    app.include_router(approvals.router)
    app.include_router(audit.router)

    @app.get("/healthz")
    def healthz() -> dict[str, str]:
        return {"status": "ok", "service": settings.app_name}

    return app
