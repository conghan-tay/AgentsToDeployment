import pytest
from app.core.settings import get_settings


@pytest.fixture(autouse=True)
def test_environment(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("ENVIRONMENT", "test")
    monkeypatch.setenv("MODEL_PROVIDER", "fake")
    monkeypatch.setenv("CHECKPOINTER_BACKEND", "memory")
    monkeypatch.setenv("KNOWLEDGE_BACKEND", "memory")
    monkeypatch.setenv("INTERNAL_API_KEY", "test-internal-key")
    monkeypatch.delenv("MCP_SERVER_URL", raising=False)
    get_settings.cache_clear()
    yield
    get_settings.cache_clear()
