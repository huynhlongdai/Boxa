"""Function-calling helpers: give Boxaz's tools to any LLM and execute its calls.

The schemas come from the server (``GET /v1/tools``), the same single source
of truth the MCP server uses, so they always match the running core.
"""

from __future__ import annotations

import json
from typing import Any, Dict, List, Mapping, Optional, Union

from ._client import AsyncBoxaz, Boxaz

__all__ = ["as_openai_tools", "as_anthropic_tools", "as_gemini_tools", "handle_tool_call", "ahandle_tool_call"]


def _client(client: Optional[Boxaz]) -> Boxaz:
    return client if client is not None else Boxaz()


def as_openai_tools(client: Optional[Boxaz] = None, *, responses_api: bool = False) -> List[Dict[str, Any]]:
    """Boxaz tools in OpenAI format.

    Chat Completions shape by default (``[{"type": "function", "function": {...}}]``);
    ``responses_api=True`` returns the flat Responses API shape
    (``[{"type": "function", "name", "description", "parameters"}]``).
    """
    tools = _client(client).tools("openai")
    if responses_api:
        return [{"type": "function", **t["function"]} for t in tools]
    return tools


def as_anthropic_tools(client: Optional[Boxaz] = None) -> List[Dict[str, Any]]:
    """Boxaz tools in Anthropic Messages API format (``name``, ``description``, ``input_schema``)."""
    return _client(client).tools("anthropic")


def as_gemini_tools(client: Optional[Boxaz] = None) -> List[Dict[str, Any]]:
    """Boxaz tools as Gemini ``Tool`` objects (``[{"functionDeclarations": [...]}]``)."""
    return _client(client).tools("gemini")


def _args(arguments: Union[str, Mapping[str, Any], None]) -> Dict[str, Any]:
    if arguments is None or arguments == "":
        return {}
    if isinstance(arguments, str):
        return json.loads(arguments)
    return dict(arguments)


def handle_tool_call(name: str, arguments: Union[str, Mapping[str, Any], None], client: Optional[Boxaz] = None) -> str:
    """Execute one tool call and return a JSON string suitable as the tool result message.
    Errors are returned (not raised) as ``{"error": {...}}`` so the model can react to the hint."""
    from ._client import BoxazError

    try:
        out = _client(client).call_tool(name, _args(arguments))
    except BoxazError as e:
        out = {"error": {"code": e.code, "message": e.message, "hint": e.hint, "retry_after_s": e.retry_after_s}}
    return out if isinstance(out, str) else json.dumps(out, ensure_ascii=False)


async def ahandle_tool_call(name: str, arguments: Union[str, Mapping[str, Any], None], client: AsyncBoxaz) -> str:
    """Async variant of :func:`handle_tool_call`."""
    from ._client import BoxazError

    try:
        out = await client.call_tool(name, _args(arguments))
    except BoxazError as e:
        out = {"error": {"code": e.code, "message": e.message, "hint": e.hint, "retry_after_s": e.retry_after_s}}
    return out if isinstance(out, str) else json.dumps(out, ensure_ascii=False)
