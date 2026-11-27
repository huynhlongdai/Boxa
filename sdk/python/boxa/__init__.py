"""Deprecated compatibility shim: the SDK was renamed ``boxa`` → ``boxmes``.

``import boxa`` keeps working (with a DeprecationWarning) and exposes the old
class names as aliases of the new ones. New code: ``from boxmes import Boxmes``.
"""

import warnings as _warnings

from boxmes import *  # noqa: F401,F403
from boxmes import AsyncBoxmes, Boxmes, BoxmesError, __version__, types  # noqa: F401

_warnings.warn("the 'boxa' package is deprecated; use 'boxmes' (Boxa → Boxmes)", DeprecationWarning, stacklevel=2)

Boxa = Boxmes
AsyncBoxa = AsyncBoxmes
BoxaError = BoxmesError
