from __future__ import annotations

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", env_prefix="GRADOPS_", extra="ignore")

    app_name: str = "grad-ops"
    database_url: str = "sqlite:///./gradops.db"
    llm_mode: str = "mock"          # mock | live
    llm_base_url: str = "https://dashscope.aliyuncs.com/compatible-mode/v1"
    llm_api_key: str = ""
    llm_model: str = "qwen-plus-latest"
    investigation_max_iterations: int = 4
    investigation_max_tool_calls: int = 12
    executor_user: str = "gradops-agent"
    allow_root_executor: bool = False
    auto_policy_refs: list[str] = []   # 允许自动执行的策略引用，空=全部人工审批
    k8s_namespaces: list[str] = ["default"]   # K8s 只读工具允许访问的命名空间


settings = Settings()
