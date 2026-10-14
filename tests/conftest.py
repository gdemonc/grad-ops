from __future__ import annotations

import os

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import create_engine
from sqlalchemy.orm import sessionmaker

os.environ["GRADOPS_DATABASE_URL"] = "sqlite:///:memory:"
os.environ["GRADOPS_LLM_MODE"] = "mock"

from app.db import Base, get_session
from app.main import build_app


@pytest.fixture()
def session():
    engine = create_engine("sqlite:///:memory:", connect_args={"check_same_thread": False})
    Base.metadata.create_all(engine)
    TestingSession = sessionmaker(bind=engine, expire_on_commit=False)
    s = TestingSession()
    yield s
    s.close()


@pytest.fixture()
def client(session):
    app = build_app()
    app.dependency_overrides[get_session] = lambda: session
    with TestClient(app) as c:
        yield c
