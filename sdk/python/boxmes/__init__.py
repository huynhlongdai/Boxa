"""Boxmes Python SDK — cheap disposable Linux boxes for AI agents.

    from boxmes import Boxmes, as_openai_tools, handle_tool_call
    bx = Boxmes()  # BOXMES_URL, BOXMES_API_KEY, BOXMES_LANG
"""

from ._client import AsyncBoxmes, Boxmes, BoxmesError
from .tools import ahandle_tool_call, as_anthropic_tools, as_gemini_tools, as_openai_tools, handle_tool_call
from . import types

__version__ = "1.1.0"

__all__ = [
    "Boxmes", "AsyncBoxmes", "BoxmesError", "as_openai_tools", "as_anthropic_tools", "as_gemini_tools",
    "handle_tool_call", "ahandle_tool_call", "types", "__version__",
]
