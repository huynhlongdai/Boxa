"""Boxaz Python SDK — cheap disposable Linux boxes for AI agents.

    from boxaz import Boxaz, as_openai_tools, handle_tool_call
    bx = Boxaz()  # BOXAZ_URL, BOXAZ_API_KEY, BOXAZ_LANG
"""

from ._client import AsyncBoxaz, Boxaz, BoxazError
from .tools import ahandle_tool_call, as_anthropic_tools, as_gemini_tools, as_openai_tools, handle_tool_call
from . import types

__version__ = "1.1.0"

__all__ = [
    "Boxaz", "AsyncBoxaz", "BoxazError", "as_openai_tools", "as_anthropic_tools", "as_gemini_tools",
    "handle_tool_call", "ahandle_tool_call", "types", "__version__",
]
