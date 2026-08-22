from functools import lru_cache
from typing import Annotated, Literal

from pydantic import Field, field_validator, model_validator
from pydantic_settings import BaseSettings, NoDecode, SettingsConfigDict


class Settings(BaseSettings):
    """Runtime settings.

    Every external dependency is configured here, so a future project can replace
    infrastructure without changing graph nodes or the Temporal workflow.
    """

    model_config = SettingsConfigDict(env_file=".env", extra="ignore", case_sensitive=False)

    environment: Literal["development", "test", "production"] = "development"
    log_level: str = "INFO"

    model_provider: Literal["openai", "anthropic", "google", "fake"] = "openai"
    model_name: str = "gpt-5-mini"
    model_temperature: float | None = None

    # Temporal owns durability, retries, and the approval pause. There is no
    # application database: the workflow event history is the source of truth.
    temporal_address: str = "localhost:7233"
    temporal_namespace: str = "default"
    temporal_task_queue: str = "support-agent"
    temporal_api_key: str = ""
    temporal_tls: bool = False

    chroma_host: str = "localhost"
    chroma_port: int = 8000
    chroma_ssl: bool = False
    chroma_collection: str = "support_knowledge"
    # Must match EMBEDDING_MODEL on the gateway: the gateway writes the vectors this
    # worker queries against, so a mismatch silently destroys retrieval quality.
    embedding_model: str = "text-embedding-3-small"
    mcp_server_url: str | None = None

    max_input_chars: int = Field(default=8_000, ge=100, le=100_000)
    max_reflection_loops: int = Field(default=1, ge=0, le=3)
    # The approval deadline is not configured here: workflow code cannot read the
    # environment, so the gateway passes it in as a workflow argument
    # (APPROVAL_TIMEOUT_HOURS in services/gateway/internal/config).
    require_approval_for: Annotated[tuple[str, ...], NoDecode] = (
        "refund",
        "account_credit",
        "cancel_order",
    )

    @field_validator("require_approval_for", mode="before")
    @classmethod
    def split_csv(cls, value: object) -> object:
        if isinstance(value, str):
            return tuple(part.strip() for part in value.split(",") if part.strip())
        return value

    @model_validator(mode="after")
    def reject_demo_production_configuration(self) -> "Settings":
        if self.environment == "production" and self.model_provider == "fake":
            raise ValueError("the fake model is not allowed in production")
        return self


@lru_cache
def get_settings() -> Settings:
    return Settings()
