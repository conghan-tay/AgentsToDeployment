import asyncio
import os
from pathlib import Path

import httpx


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
    base_url = os.getenv("API_BASE_URL", "http://localhost:8080")
    api_key = os.getenv("API_KEY", "local-api-key")
    async with httpx.AsyncClient(base_url=base_url, timeout=30) as client:
        response = await client.post(
            "/v1/knowledge", json={"documents": documents}, headers={"X-API-Key": api_key}
        )
        response.raise_for_status()
        print(response.json())


if __name__ == "__main__":
    asyncio.run(main())
