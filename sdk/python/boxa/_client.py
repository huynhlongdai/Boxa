"""Boxa clients (sync ``Boxa`` and async ``AsyncBoxa``).

Uses httpx when installed; falls back to the standard library (urllib) so the
SDK works with zero dependencies. Both clients expose the same methods; the
async ones return awaitables.
"""

from __future__ import annotations

import asyncio
import json as _json
import os
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import TYPE_CHECKING, Any, Dict, Iterable, List, Mapping, Optional, Tuple, Union

from .types import (
    Agent, AgentKind, ApiKey, BatchCommand, BatchResult, Box, Event, ExecResult, Job, JobRef, Quote,
    Snapshot, Snippet, Template,
)

try:  # optional dependency
    import httpx  # type: ignore
except Exception:  # pragma: no cover - exercised when httpx is absent
    httpx = None  # type: ignore

__all__ = ["Boxa", "AsyncBoxa", "BoxaError"]

DEFAULT_URL = "http://localhost:8080"
RETRYABLE = {"NO_CAPACITY", "PROVIDER_FAILED", "RATE_LIMITED", "INTERNAL"}


class BoxaError(Exception):
    """Error envelope from the API: ``{"error": {code, message, hint, retry_after_s?, docs_url?}}``.

    ``code`` is stable across languages; ``message``/``hint`` follow the
    client's ``lang`` (Accept-Language).
    """

    def __init__(self, status: int, code: str, message: str, hint: str = "",
                 retry_after_s: Optional[int] = None, docs_url: Optional[str] = None, body: Any = None):
        self.status, self.code, self.message, self.hint = status, code, message, hint
        self.retry_after_s, self.docs_url, self.body = retry_after_s, docs_url, body
        super().__init__(f"{code}: {message}" + (f" (hint: {hint})" if hint else ""))

    @property
    def retryable(self) -> bool:
        return self.code in RETRYABLE


def _error_from(status: int, body: bytes) -> BoxaError:
    try:
        env = _json.loads(body.decode() or "{}").get("error") or {}
    except Exception:
        env = {}
    if not env.get("code"):
        return BoxaError(status, str(status), body.decode(errors="replace").strip()[:500])
    return BoxaError(status, env.get("code", ""), env.get("message", ""), env.get("hint", ""),
                     env.get("retry_after_s"), env.get("docs_url"), env)


def _clean(d: Mapping[str, Any]) -> Dict[str, Any]:
    return {k: v for k, v in d.items() if v is not None}


def _box_body(template: Optional[str], lifecycle: Optional[str], cpu: Optional[float], ram_mb: Optional[int],
              disk_gb: Optional[int], extra: Mapping[str, Any]) -> Dict[str, Any]:
    if extra.get("yaml"):
        return _clean({"yaml": extra["yaml"], "agent_id": extra.get("agent_id")})
    spec = _clean({"cpu": cpu, "ram_mb": ram_mb, "disk_gb": disk_gb})
    body = _clean({"template": template, "lifecycle": lifecycle, "spec": spec or None})
    body.update(_clean(dict(extra)))
    return body


def _q(v: str) -> str:
    return urllib.parse.quote(v, safe="")


class _Base:
    """Request building shared by both clients. Methods return whatever
    ``self._call`` returns (a value for Boxa, an awaitable for AsyncBoxa)."""

    def __init__(self, api_key: Optional[str] = None, base_url: Optional[str] = None, *, lang: Optional[str] = None,
                 timeout: float = 600.0, max_retries: int = 2, user_agent: str = "boxa-python/1.1"):
        self.api_key = api_key if api_key is not None else os.environ.get("BOXA_API_KEY", "")
        url = (base_url or os.environ.get("BOXA_URL") or DEFAULT_URL).rstrip("/")
        if url.endswith("/v1"):
            url = url[:-3]
        self.base_url = url
        self.lang = lang or os.environ.get("BOXA_LANG") or None
        self.timeout = timeout
        self.max_retries = max_retries
        self.user_agent = user_agent
        host = (urllib.parse.urlparse(self.base_url).hostname or "").lower()
        # Never send loopback traffic through HTTP(S)_PROXY.
        self._loopback = host in ("localhost", "::1") or host.startswith("127.")

    # -- plumbing ---------------------------------------------------------
    def _headers(self, extra: Optional[Mapping[str, str]] = None) -> Dict[str, str]:
        h = {"User-Agent": self.user_agent, "Accept": "application/json"}
        if self.api_key:
            h["Authorization"] = f"Bearer {self.api_key}"
        if self.lang:
            h["Accept-Language"] = self.lang
        if extra:
            h.update(extra)
        return h

    def _url(self, path: str, params: Optional[Mapping[str, Any]] = None) -> str:
        p = path if path.startswith("/") and not path.startswith("/v1/") and path in (
            "/llms.txt", "/llms-full.txt", "/.well-known/mcp.json", "/.well-known/agent.json", "/mcp") else "/v1" + path
        u = self.base_url + p
        if params:
            q = {k: ("true" if v is True else "false" if v is False else v) for k, v in params.items() if v is not None}
            if q:
                u += "?" + urllib.parse.urlencode(q)
        return u

    def _call(self, method: str, path: str, body: Any = None, params: Optional[Mapping[str, Any]] = None,
              headers: Optional[Mapping[str, str]] = None, raw: bool = False) -> Any:  # pragma: no cover
        raise NotImplementedError

    # -- discovery (no auth) ---------------------------------------------
    def health(self) -> Dict[str, Any]:
        return self._call("GET", "/health")

    def tools(self, format: str = "openai") -> Any:
        """Tool schemas (single source of truth with the MCP server)."""
        return self._call("GET", "/tools", params={"format": format})

    def agent_kinds(self) -> Dict[str, List[AgentKind]]:
        return self._call("GET", "/agent-kinds")

    def openapi(self) -> Dict[str, Any]:
        return self._call("GET", "/openapi.json")

    def llms_txt(self, full: bool = False) -> str:
        return self._call("GET", "/llms-full.txt" if full else "/llms.txt", raw=True)

    def errors(self) -> Dict[str, Any]:
        return self._call("GET", "/errors")

    # -- boxes -------------------------------------------------------------
    def quote(self, template: Optional[str] = None, lifecycle: Optional[str] = None, *, cpu: Optional[float] = None,
              ram_mb: Optional[int] = None, disk_gb: Optional[int] = None, **extra: Any) -> Quote:
        """Dry-run placement + price; nothing is created. Returns :class:`Quote`."""
        return self._call("POST", "/quote", _box_body(template, lifecycle, cpu, ram_mb, disk_gb, extra))

    def create_box(self, template: Optional[str] = "base", lifecycle: Optional[str] = "session", *,
                   cpu: Optional[float] = None, ram_mb: Optional[int] = None, disk_gb: Optional[int] = None,
                   idempotency_key: Optional[str] = None, wait: bool = True, **extra: Any) -> Box:
        """Create a box (``yaml=...`` also accepted). Extra fields: slug, ttl_seconds, idle_minutes,
        budget_usd, ports, secrets, setup, region, agent_id. Returns :class:`Box` (with ``next`` hints)."""
        h = {"Idempotency-Key": idempotency_key} if idempotency_key else None
        return self._call("POST", "/boxes", _box_body(template, lifecycle, cpu, ram_mb, disk_gb, extra),
                          params=None if wait else {"wait": False}, headers=h)

    up = create_box

    def list_boxes(self, *, status: Optional[str] = None, lifecycle: Optional[str] = None,
                   agent: Optional[str] = None, provider: Optional[str] = None, all: Optional[bool] = None) -> Dict[str, List[Box]]:
        return self._call("GET", "/boxes", params={"status": status, "lifecycle": lifecycle, "agent": agent,
                                                   "provider": provider, "all": all})

    def get_box(self, box_id: str) -> Box:
        return self._call("GET", f"/boxes/{_q(box_id)}")

    def delete_box(self, box_id: str) -> Box:
        return self._call("DELETE", f"/boxes/{_q(box_id)}")

    down = delete_box

    def sleep(self, box_id: str) -> Box:
        return self._call("POST", f"/boxes/{_q(box_id)}/sleep", {})

    def wake(self, box_id: str) -> Box:
        return self._call("POST", f"/boxes/{_q(box_id)}/wake", {})

    def box_yaml(self, box_id: str) -> Dict[str, str]:
        return self._call("GET", f"/boxes/{_q(box_id)}/yaml")

    def events(self, box_id: str) -> Dict[str, List[Event]]:
        return self._call("GET", f"/boxes/{_q(box_id)}/events")

    # -- exec --------------------------------------------------------------
    def exec(self, box_id: str, command: str, *, cwd: Optional[str] = None, timeout_s: Optional[int] = None,
             tail_lines: Optional[int] = None, async_: bool = False) -> Union[ExecResult, JobRef]:
        """Run a command. Sync → :class:`ExecResult`; ``async_=True`` → ``{"job_id"}``."""
        body = _clean({"command": command, "cwd": cwd, "timeout_s": timeout_s, "tail_lines": tail_lines})
        if async_:
            body["async"] = True
        return self._call("POST", f"/boxes/{_q(box_id)}/exec", body)

    def exec_batch(self, box_id: str, commands: Iterable[BatchCommand], *, stop_on_error: bool = True,
                   cwd: Optional[str] = None, timeout_s: Optional[int] = None, tail_lines: Optional[int] = None) -> BatchResult:
        """Run several commands in one round-trip. Returns :class:`BatchResult`."""
        body = _clean({"commands": list(commands), "stop_on_error": stop_on_error, "cwd": cwd,
                       "timeout_s": timeout_s, "tail_lines": tail_lines})
        return self._call("POST", f"/boxes/{_q(box_id)}/exec-batch", body)

    def job(self, job_id: str, wait: Optional[int] = None) -> Job:
        """Get a job; ``wait`` long-polls up to that many seconds (max 60)."""
        return self._call("GET", f"/jobs/{_q(job_id)}", params={"wait": wait})

    def logs(self, log_id: str) -> str:
        """Full output of an exec (text)."""
        return self._call("GET", f"/logs/{_q(log_id)}", raw=True)

    # -- files ---------------------------------------------------------------
    def read(self, box_id: str, path: str, *, from_line: Optional[int] = None, to_line: Optional[int] = None) -> Dict[str, Any]:
        return self._call("GET", f"/boxes/{_q(box_id)}/files", params={"path": path, "from": from_line, "to": to_line})

    def write(self, box_id: str, path: str, content: str) -> Dict[str, Any]:
        return self._call("PUT", f"/boxes/{_q(box_id)}/files", {"path": path, "content": content})

    def grep(self, box_id: str, pattern: str, path: Optional[str] = None) -> Dict[str, Any]:
        return self._call("POST", f"/boxes/{_q(box_id)}/grep", _clean({"pattern": pattern, "path": path}))

    def observe(self, box_id: str) -> Dict[str, Any]:
        return self._call("GET", f"/boxes/{_q(box_id)}/observe")

    # -- snapshots -------------------------------------------------------------
    def snapshot(self, box_id: str) -> Snapshot:
        return self._call("POST", f"/boxes/{_q(box_id)}/snapshot", {})

    def snapshots(self, box_id: str) -> Dict[str, List[Snapshot]]:
        return self._call("GET", f"/boxes/{_q(box_id)}/snapshots")

    def restore(self, box_id: str, snapshot_id: Optional[str] = None) -> Box:
        return self._call("POST", f"/boxes/{_q(box_id)}/restore", _clean({"snapshot_id": snapshot_id}))

    def fork(self, box_id: str, snapshot_id: Optional[str] = None, *, slug: Optional[str] = None) -> Box:
        return self._call("POST", f"/boxes/{_q(box_id)}/fork", _clean({"snapshot_id": snapshot_id, "slug": slug}))

    # -- misc ----------------------------------------------------------------
    def templates(self) -> Dict[str, List[Template]]:
        return self._call("GET", "/templates")

    def overview(self) -> Dict[str, Any]:
        return self._call("GET", "/overview")

    def snippet(self, agent_id: str, kind: Optional[str] = None, lang: Optional[str] = None) -> Snippet:
        return self._call("GET", f"/agents/{_q(agent_id)}/snippet", params={"kind": kind, "lang": lang})

    # admin
    def create_agent(self, name: str, kind: str = "custom", **fields: Any) -> Agent:
        return self._call("POST", "/agents", {"name": name, "kind": kind, **fields})

    def create_key(self, agent_id: Optional[str], name: str = "sdk", scopes: Optional[List[str]] = None) -> ApiKey:
        return self._call("POST", "/keys", _clean({"agent_id": agent_id, "name": name, "scopes": scopes}))

    # -- MCP tool bridge -------------------------------------------------------
    def _mcp_body(self, name: str, arguments: Mapping[str, Any]) -> Dict[str, Any]:
        return {"jsonrpc": "2.0", "id": int(time.time() * 1000), "method": "tools/call",
                "params": {"name": name, "arguments": dict(arguments)}}

    @staticmethod
    def _mcp_result(resp: Mapping[str, Any]) -> Any:
        if resp.get("error"):
            raise BoxaError(400, "INVALID", resp["error"].get("message", "MCP error"))
        res = resp.get("result") or {}
        out = res.get("structuredContent")
        if out is None:
            text = "".join(c.get("text", "") for c in res.get("content", []))
            try:
                out = _json.loads(text)
            except Exception:
                out = text
        if res.get("isError") and isinstance(out, dict) and "error" in out:
            e = out["error"]
            raise BoxaError(400, e.get("code", "INVALID"), e.get("message", ""), e.get("hint", ""),
                            e.get("retry_after_s"), e.get("docs_url"), out)
        return out


def _parse(status: int, ctype: str, body: bytes, raw: bool) -> Any:
    if status >= 300:
        raise _error_from(status, body)
    if raw or "json" not in ctype:
        return body.decode("utf-8", errors="replace")
    return _json.loads(body.decode() or "null")


def _retry_wait(err: BoxaError, attempt: int) -> float:
    if err.retry_after_s:
        return min(float(err.retry_after_s), 30.0)
    return min(0.5 * (2 ** attempt), 8.0)


class Boxa(_Base):
    """Synchronous client.

    >>> bx = Boxa()                       # BOXA_URL / BOXA_API_KEY / BOXA_LANG from env
    >>> box = bx.create_box("python", "task", ttl_seconds=600)
    >>> bx.exec(box["id"], "python3 -c 'print(42)'")["stdout_tail"]
    '42'
    """

    def __init__(self, *args: Any, **kwargs: Any):
        super().__init__(*args, **kwargs)
        self._http = httpx.Client(timeout=self.timeout, trust_env=not self._loopback) if httpx is not None else None
        handlers = [urllib.request.ProxyHandler({})] if self._loopback else []
        self._opener = urllib.request.build_opener(*handlers)

    def close(self) -> None:
        if self._http is not None:
            self._http.close()

    def __enter__(self) -> "Boxa":
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()

    def _send(self, method: str, url: str, data: Optional[bytes], headers: Dict[str, str]) -> Tuple[int, str, bytes]:
        if self._http is not None:
            r = self._http.request(method, url, content=data, headers=headers)
            return r.status_code, r.headers.get("content-type", ""), r.content
        req = urllib.request.Request(url, data=data, method=method, headers=headers)
        try:
            with self._opener.open(req, timeout=self.timeout) as r:
                return r.status, r.headers.get("Content-Type", ""), r.read()
        except urllib.error.HTTPError as e:
            return e.code, e.headers.get("Content-Type", ""), e.read()

    def _call(self, method, path, body=None, params=None, headers=None, raw=False):
        url = self._url(path, params)
        data = None
        h = self._headers(headers)
        if body is not None:
            data = _json.dumps(body).encode()
            h["Content-Type"] = "application/json"
        attempt = 0
        while True:
            st, ctype, content = self._send(method, url, data, h)
            try:
                return _parse(st, ctype, content, raw)
            except BoxaError as e:
                if not e.retryable or attempt >= self.max_retries:
                    raise
                time.sleep(_retry_wait(e, attempt))
                attempt += 1

    # helpers needing post-processing
    def run(self, box_id: str, command: str, *, poll_s: int = 30, **kw: Any) -> Job:
        """Start ``command`` async and long-poll until it finishes."""
        ref = self.exec(box_id, command, async_=True, **kw)
        while True:
            j = self.job(ref["job_id"], wait=poll_s)
            if j["status"] in ("done", "failed"):
                return j

    def call_tool(self, name: str, arguments: Optional[Mapping[str, Any]] = None) -> Any:
        """Execute a Boxa tool by name (as returned by :func:`as_openai_tools`) through the MCP
        endpoint, so arguments are mapped exactly like the MCP server does."""
        resp = self._call("POST", "/mcp", self._mcp_body(name, arguments or {}))
        return self._mcp_result(resp)


class AsyncBoxa(_Base):
    """Asynchronous client (httpx.AsyncClient, or urllib in a worker thread)."""

    def __init__(self, *args: Any, **kwargs: Any):
        super().__init__(*args, **kwargs)
        self._http = httpx.AsyncClient(timeout=self.timeout, trust_env=not self._loopback) if httpx is not None else None
        self._sync = None if httpx is not None else Boxa(*args, **kwargs)

    async def aclose(self) -> None:
        if self._http is not None:
            await self._http.aclose()

    async def __aenter__(self) -> "AsyncBoxa":
        return self

    async def __aexit__(self, *exc: Any) -> None:
        await self.aclose()

    async def _call(self, method, path, body=None, params=None, headers=None, raw=False):
        if self._http is None:
            return await asyncio.to_thread(self._sync._call, method, path, body, params, headers, raw)  # type: ignore
        url = self._url(path, params)
        h = self._headers(headers)
        data = None
        if body is not None:
            data = _json.dumps(body).encode()
            h["Content-Type"] = "application/json"
        attempt = 0
        while True:
            r = await self._http.request(method, url, content=data, headers=h)
            try:
                return _parse(r.status_code, r.headers.get("content-type", ""), r.content, raw)
            except BoxaError as e:
                if not e.retryable or attempt >= self.max_retries:
                    raise
                await asyncio.sleep(_retry_wait(e, attempt))
                attempt += 1

    async def run(self, box_id: str, command: str, *, poll_s: int = 30, **kw: Any) -> Job:
        ref = await self.exec(box_id, command, async_=True, **kw)
        while True:
            j = await self.job(ref["job_id"], wait=poll_s)
            if j["status"] in ("done", "failed"):
                return j

    async def call_tool(self, name: str, arguments: Optional[Mapping[str, Any]] = None) -> Any:
        resp = await self._call("POST", "/mcp", self._mcp_body(name, arguments or {}))
        return self._mcp_result(resp)

    if TYPE_CHECKING:  # precise async signatures for type checkers (generated from _Base)
        async def health(self) -> Dict[str, Any]: ...
        async def tools(self, format: str = "openai") -> Any: ...
        async def agent_kinds(self) -> Dict[str, List[AgentKind]]: ...
        async def openapi(self) -> Dict[str, Any]: ...
        async def llms_txt(self, full: bool = False) -> str: ...
        async def errors(self) -> Dict[str, Any]: ...
        async def quote(self, template: Optional[str] = None, lifecycle: Optional[str] = None, *, cpu: Optional[float] = None, ram_mb: Optional[int] = None, disk_gb: Optional[int] = None, **extra: Any) -> Quote: ...
        async def create_box(self, template: Optional[str] = "base", lifecycle: Optional[str] = "session", *, cpu: Optional[float] = None, ram_mb: Optional[int] = None, disk_gb: Optional[int] = None, idempotency_key: Optional[str] = None, wait: bool = True, **extra: Any) -> Box: ...
        async def list_boxes(self, *, status: Optional[str] = None, lifecycle: Optional[str] = None, agent: Optional[str] = None, provider: Optional[str] = None, all: Optional[bool] = None) -> Dict[str, List[Box]]: ...
        async def get_box(self, box_id: str) -> Box: ...
        async def delete_box(self, box_id: str) -> Box: ...
        async def sleep(self, box_id: str) -> Box: ...
        async def wake(self, box_id: str) -> Box: ...
        async def box_yaml(self, box_id: str) -> Dict[str, str]: ...
        async def events(self, box_id: str) -> Dict[str, List[Event]]: ...
        async def exec(self, box_id: str, command: str, *, cwd: Optional[str] = None, timeout_s: Optional[int] = None, tail_lines: Optional[int] = None, async_: bool = False) -> Union[ExecResult, JobRef]: ...
        async def exec_batch(self, box_id: str, commands: Iterable[BatchCommand], *, stop_on_error: bool = True, cwd: Optional[str] = None, timeout_s: Optional[int] = None, tail_lines: Optional[int] = None) -> BatchResult: ...
        async def job(self, job_id: str, wait: Optional[int] = None) -> Job: ...
        async def logs(self, log_id: str) -> str: ...
        async def read(self, box_id: str, path: str, *, from_line: Optional[int] = None, to_line: Optional[int] = None) -> Dict[str, Any]: ...
        async def write(self, box_id: str, path: str, content: str) -> Dict[str, Any]: ...
        async def grep(self, box_id: str, pattern: str, path: Optional[str] = None) -> Dict[str, Any]: ...
        async def observe(self, box_id: str) -> Dict[str, Any]: ...
        async def snapshot(self, box_id: str) -> Snapshot: ...
        async def snapshots(self, box_id: str) -> Dict[str, List[Snapshot]]: ...
        async def restore(self, box_id: str, snapshot_id: Optional[str] = None) -> Box: ...
        async def fork(self, box_id: str, snapshot_id: Optional[str] = None, *, slug: Optional[str] = None) -> Box: ...
        async def templates(self) -> Dict[str, List[Template]]: ...
        async def overview(self) -> Dict[str, Any]: ...
        async def snippet(self, agent_id: str, kind: Optional[str] = None, lang: Optional[str] = None) -> Snippet: ...
        async def create_agent(self, name: str, kind: str = "custom", **fields: Any) -> Agent: ...
        async def create_key(self, agent_id: Optional[str], name: str = "sdk", scopes: Optional[List[str]] = None) -> ApiKey: ...
        up = create_box
        down = delete_box


# Typed aliases for readers (methods return these shapes).
_ = (Agent, AgentKind, ApiKey, BatchResult, Box, Event, ExecResult, JobRef, Quote, Snapshot, Snippet, Template)
