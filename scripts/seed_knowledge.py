import asyncio
import os
from pathlib import Path

import httpx
from dotenv import dotenv_values


def load_seed_settings(root: Path) -> tuple[str, str]:
    """Load the same project .env file that Docker Compose reads automatically.

    Existing exported variables still win, which makes API_BASE_URL/API_KEY overrides
    useful for seeding a remote development or staging environment.
    """

    dotenv = dotenv_values(root / ".env")
    return (
        os.getenv("API_BASE_URL") or dotenv.get("API_BASE_URL") or "http://localhost:8080",
        os.getenv("API_KEY") or dotenv.get("API_KEY") or "local-api-key",
    )


async def main() -> None:
    root = Path(__file__).resolve().parents[1]
    documents = []
    for path in sorted((root / "data" / "knowledge").glob("*.md")):
        documents.append(
            {
                "id": path.stem,
                "title": path.stem.replace("-", " ").title(),
                "content": path.read_text(),
                "source": path.stem,
            }
        )
    base_url, api_key = load_seed_settings(root)
    async with httpx.AsyncClient(base_url=base_url, timeout=30) as client:
        response = await client.post(
            "/v1/knowledge", json={"documents": documents}, headers={"X-API-Key": api_key}
        )
        if response.status_code == 401:
            raise SystemExit(
                "Knowledge seeding was rejected (401). Ensure API_KEY in the project "
                ".env matches the key used to start the gateway, then restart the stack."
            )
        response.raise_for_status()
        print(response.json())


if __name__ == "__main__":
    asyncio.run(main())
