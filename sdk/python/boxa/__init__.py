"""Boxa Python SDK — cheap disposable Linux boxes for AI agents.

    from boxa import Boxa, as_openai_tools, handle_tool_call
    bx = Boxa()  # BOXA_URL, BOXA_API_KEY, BOXA_LANG
"""

from ._client import AsyncBoxa, Boxa, BoxaError
from .tools import ahandle_tool_call, as_anthropic_tools, as_gemini_tools, as_openai_tools, handle_tool_call
from . import types

__version__ = "1.1.0"

__all__ = [
    "Boxa", "AsyncBoxa", "BoxaError", "as_openai_tools", "as_anthropic_tools", "as_gemini_tools",
    "handle_tool_call", "ahandle_tool_call", "types", "__version__",
]
