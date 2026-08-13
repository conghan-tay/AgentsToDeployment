from functools import lru_cache
from typing import Literal

from pydantic import Field, field_validator, model_validator
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """Runtime settings.

    Every external dependency is configured here, so a future project can replace
    infrastructure without changing graph nodes or HTTP handlers.
    """

    model_config = SettingsConfigDict(env_file=".env", extra="ignore", case_sensitive=False)

    environment: Literal["development", "test", "production"] = "development"
    log_level: str = "INFO"
    internal_api_key: str = "local-internal-key"

    model_provider: Literal["openai", "anthropic", "google", "fake"] = "openai"
    model_name: str = "gpt-5-mini"
    model_temperature: float | None = None

    postgres_dsn: str = "postgresql://support:support@localhost:5432/support?sslmode=disable"
    redis_url: str = "redis://localhost:6379/0"
    chroma_host: str = "localhost"
    chroma_port: int = 8001
    chroma_ssl: bool = False
    chroma_collection: str = "support_knowledge"
    mcp_server_url: str | None = None

    checkpointer_backend: Literal["postgres", "memory"] = "postgres"
    knowledge_backend: Literal["chroma", "memory"] = "chroma"
    max_input_chars: int = Field(default=8_000, ge=100, le=100_000)
    max_reflection_loops: int = Field(default=1, ge=0, le=3)
    require_approval_for: tuple[str, ...] = ("refund", "account_credit", "cancel_order")
    allowed_origins: tuple[str, ...] = ("http://localhost:3000",)

    @field_validator("require_approval_for", "allowed_origins", mode="before")
    @classmethod
    def split_csv(cls, value: object) -> object:
        if isinstance(value, str):
            return tuple(part.strip() for part in value.split(",") if part.strip())
        return value

    @model_validator(mode="after")
    def reject_demo_production_configuration(self) -> "Settings":
        if self.environment == "production" and self.internal_api_key == "local-internal-key":
            raise ValueError("INTERNAL_API_KEY must be replaced in production")
        if self.environment == "production" and self.model_provider == "fake":
            raise ValueError("the fake model is not allowed in production")
        return self


@lru_cache
def get_settings() -> Settings:
    return Settings()
