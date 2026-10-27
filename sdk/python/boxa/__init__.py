"""Deprecated compatibility shim: the SDK was renamed ``boxa`` → ``boxaz``.

``import boxa`` keeps working (with a DeprecationWarning) and exposes the old
class names as aliases of the new ones. New code: ``from boxaz import Boxaz``.
"""

import warnings as _warnings

from boxaz import *  # noqa: F401,F403
from boxaz import AsyncBoxaz, Boxaz, BoxazError, __version__, types  # noqa: F401

_warnings.warn("the 'boxa' package is deprecated; use 'boxaz' (Boxa → Boxaz)", DeprecationWarning, stacklevel=2)

Boxa = Boxaz
AsyncBoxa = AsyncBoxaz
BoxaError = BoxazError
