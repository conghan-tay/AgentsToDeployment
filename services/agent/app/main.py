from collections.abc import AsyncIterator
from contextlib import AsyncExitStack, asynccontextmanager

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from langgraph.checkpoint.memory import InMemorySaver
from langgraph.checkpoint.postgres.aio import AsyncPostgresSaver

from .api.routes import router
from .core.logging import configure_logging
from .core.models import build_model
from .core.settings import get_settings
from .graph.workflow import build_graph
from .knowledge.repository import build_knowledge_repository
from .tools.registry import ToolRegistry


@asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncIterator[None]:
    settings = get_settings()
    configure_logging(settings.log_level)
    stack = AsyncExitStack()
    try:
        if settings.checkpointer_backend == "memory":
            checkpointer = InMemorySaver()
        else:
            checkpointer = await stack.enter_async_context(
                AsyncPostgresSaver.from_conn_string(settings.postgres_dsn)
            )
            # setup() is idempotent and creates LangGraph checkpoint tables on first boot.
            await checkpointer.setup()

        knowledge = build_knowledge_repository(settings)
        tools = await ToolRegistry.create(settings)
        app.state.knowledge = knowledge
        app.state.graph = build_graph(
            settings=settings,
            model=build_model(settings),
            knowledge=knowledge,
            tools=tools,
            checkpointer=checkpointer,
        )
        yield
    finally:
        await stack.aclose()


def create_app() -> FastAPI:
    settings = get_settings()
    app = FastAPI(
        title="Support Agent Service",
        version="0.1.0",
        description="Internal LangGraph runtime. Use the Go gateway for public traffic.",
        lifespan=lifespan,
    )
    app.add_middleware(
        CORSMiddleware,
        allow_origins=list(settings.allowed_origins),
        allow_credentials=False,
        allow_methods=["GET", "POST"],
        allow_headers=["Content-Type", "X-Request-ID", "X-Internal-API-Key"],
    )
    app.include_router(router)
    return app


app = create_app()
