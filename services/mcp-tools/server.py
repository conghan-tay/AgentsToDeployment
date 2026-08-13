import os
from typing import Any

from mcp.server.fastmcp import FastMCP

mcp = FastMCP(
    "support-business-tools",
    host="0.0.0.0",
    port=int(os.getenv("MCP_PORT", "8001")),
)


@mcp.tool()
def lookup_order(order_id: str) -> dict[str, Any]:
    """Return order status from the demo order system."""

    # Replace this body with the commerce provider SDK. The MCP contract and graph
    # do not need to change when the implementation moves to another repository.
    suffix = sum(ord(character) for character in order_id) % 3
    states = (
        {"status": "processing", "eta_business_days": 5},
        {"status": "in_transit", "eta_business_days": 3},
        {"status": "delivered", "eta_business_days": 0},
    )
    return {"order_id": order_id, **states[suffix]}


@mcp.tool()
def get_shipping_region(postal_code: str) -> dict[str, str]:
    """Return a coarse demo shipping region for a postal code."""

    return {"postal_code": postal_code, "region": "standard"}


if __name__ == "__main__":
    mcp.run(transport="streamable-http")
