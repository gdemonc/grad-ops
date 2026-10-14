from __future__ import annotations

import sys

from app.config import settings
from app.db import SessionLocal, init_db
from app.worker import Worker


def main() -> int:
    init_db()
    session = SessionLocal()
    worker = Worker(session)
    print(f"grad-ops worker 启动 worker_id={worker.worker_id} database={settings.database_url}")
    try:
        worker.run_forever()
    except KeyboardInterrupt:
        print("收到中断信号，worker 停止")
    finally:
        session.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
