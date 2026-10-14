#!/usr/bin/env bash
uvicorn app.main:build_app --factory --reload --port 8000
