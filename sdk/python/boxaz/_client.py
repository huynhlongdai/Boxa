"""Boxaz clients (sync ``Boxaz`` and async ``AsyncBoxaz``).

Uses httpx when installed; falls back to the standard library (urllib) so the
SDK works with zero dependencies. Both clients expose the same methods; the
async ones return awaitables.
"""

from __future__ import annotations

import asyncio
import json as _json
import os
import inspect
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

__all__ = ["Boxaz", "AsyncBoxaz", "BoxazError"]

def _env(name: str) -> "str | None":
    """BOXAZ_<name>, falling back to the pre-rename BOXA_<name> (deprecated)."""
    v = os.environ.get("BOXAZ_" + name)
    if v:
        return v
    old = os.environ.get("BOXA_" + name)
    if old:
        import warnings

        warnings.warn(f"BOXA_{name} is deprecated; rename it to BOXAZ_{name}", DeprecationWarning, stacklevel=3)
        return old
    return None


DEFAULT_URL = "http://localhost:8080"
RETRYABLE = {"NO_CAPACITY", "PROVIDER_FAILED", "RATE_LIMITED", "INTERNAL"}


class BoxazError(Exception):
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


def _error_from(status: int, body: bytes) -> BoxazError:
    try:
        env = _json.loads(body.decode() or "{}").get("error") or {}
    except Exception:
        env = {}
    if not env.get("code"):
        return BoxazError(status, str(status), body.decode(errors="replace").strip()[:500])
    return BoxazError(status, env.get("code", ""), env.get("message", ""), env.get("hint", ""),
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
    ``self._call`` returns (a value for Boxaz, an awaitable for AsyncBoxaz)."""

    def __init__(self, api_key: Optional[str] = None, base_url: Optional[str] = None, *, lang: Optional[str] = None,
                 timeout: float = 600.0, max_retries: int = 2, user_agent: str = "boxaz-python/1.1"):
        self.api_key = api_key if api_key is not None else _env("API_KEY") or ""
        url = (base_url or _env("URL") or DEFAULT_URL).rstrip("/")
        if url.endswith("/v1"):
            url = url[:-3]
        self.base_url = url
        self.lang = lang or _env("LANG") or None
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
        budget_usd, ports, secrets, setup, region, agent_id, subdomain (v1.9 gateway name → https://<subdomain>.<domain>/). Returns :class:`Box` (with ``next`` hints)."""
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
             tail_lines: Optional[int] = None, async_: bool = False, session: bool = False) -> Union[ExecResult, JobRef]:
        """Run a command. Sync → :class:`ExecResult`; ``async_=True`` → ``{"job_id"}``.
        ``session=True`` keeps cwd and exported variables between calls (persistent shell)."""
        body = _clean({"command": command, "cwd": cwd, "timeout_s": timeout_s, "tail_lines": tail_lines})
        if async_:
            body["async"] = True
        if session:
            body["session"] = True
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

    # -- v1.3: need-based selection + agent helpers ---------------------------
    def profiles(self) -> Dict[str, Any]:
        """Workload profiles and per-agent-kind defaults (no auth needed)."""
        return self._call("GET", "/profiles")

    def recommend(self, profile: Optional[str] = None, *, template: Optional[str] = None,
                  lifecycle: Optional[str] = None, untrusted: Optional[bool] = None, fork: Optional[bool] = None,
                  **requirements: Any) -> Dict[str, Any]:
        """Rank placement options (warm pool / pool container / VM / new server / v1.4 sandbox API)
        with price, ETA, score and reasons. ``requirements``: cpu, ram_mb, disk_gb, max_price_hour,
        location, max_latency_ms, optimize, isolation (any|container|vm|sandbox), arch, region,
        lifetime, duration_hours, public_url. ``untrusted=True`` keeps only own-kernel options
        (VM, microVM/gVisor sandboxes); ``fork=True`` keeps only options that can fork/clone.
        Sandbox options carry ``tier`` (T2/T3), ``fork``, ``pause``, ``ports``, ``max_lifetime_h``,
        ``verification`` and ``strong_isolation``. Creates nothing."""
        req = dict(requirements)
        if untrusted is not None:
            req["untrusted"] = untrusted
        if fork is not None:
            req["fork"] = fork
        return self._call("POST", "/recommend", _clean({"profile": profile, "template": template, "lifecycle": lifecycle,
                                                        "requirements": _clean(req) or None}))

    # -- v1.4: sandbox API providers (T2 dial-in / T3 exec-only shim) ---------
    def sandbox_providers(self) -> Dict[str, List[Dict[str, Any]]]:
        """Every sandbox provider Core knows: adapter (native|yaml), tier, effective ``mode``
        (dial = T2, shim = T3), capabilities (boot_ms, fork, pause, ports, gpu, max_lifetime_s,
        isolation), per-second ``pricing``, sizes, regions, ``verification`` level
        (live-verified | docs-verified | sdk-source) and connection state."""
        return self._call("GET", "/sandbox/providers")

    def lint_adapter(self, yaml: str) -> Dict[str, Any]:
        """Check a declarative (YAML) sandbox adapter without loading it (admin key).
        Returns ``{ok, error?, info?}``; ``info`` is the provider metadata the adapter declares."""
        return self._call("POST", "/sandbox/adapters/lint", {"yaml": yaml})

    def ensure(self, name: str, profile: Optional[str] = None, *, template: Optional[str] = None,
               lifecycle: Optional[str] = None, cpu: Optional[float] = None, ram_mb: Optional[int] = None,
               disk_gb: Optional[int] = None, requirements: Optional[Mapping[str, Any]] = None, **extra: Any) -> Dict[str, Any]:
        """Get-or-create a box by name (idempotent): returns ``{box, action, created, resume?}``;
        a sleeping box is woken and comes with a resume summary."""
        body = _box_body(template, lifecycle, cpu, ram_mb, disk_gb, extra)
        body.update(_clean({"name": name, "profile": profile, "requirements": dict(requirements) if requirements else None}))
        return self._call("POST", "/boxes/ensure", body)

    def resume(self, box_id: str) -> Dict[str, Any]:
        """Wake if needed and summarise recent execs, running jobs, changed files and ports."""
        return self._call("GET", f"/boxes/{_q(box_id)}/resume")

    def clone(self, box_id: str, *, slug: Optional[str] = None) -> Box:
        """Copy-on-write clone of a live box on the same host."""
        return self._call("POST", f"/boxes/{_q(box_id)}/clone", _clean({"slug": slug}))

    def rightsize(self, box_id: Optional[str] = None) -> Dict[str, Any]:
        """Right-sizing suggestion for one box (or all running boxes)."""
        return self._call("GET", f"/boxes/{_q(box_id)}/rightsize" if box_id else "/rightsize")

    # -- v1.7: supervised app, gateway URLs, URL tokens, wake settings ------------
    def set_app(self, box_id: str, start: str, *, repo: Optional[str] = None, ref: Optional[str] = None,
                install: Optional[str] = None, port: Optional[int] = None, ready_http: Optional[str] = None,
                ready_tcp: bool = False, restart: Optional[str] = None, env: Optional[Mapping[str, str]] = None,
                workdir: Optional[str] = None, backoff_max_s: Optional[int] = None, label: Optional[str] = None) -> Dict[str, Any]:
        """Declare the box's long-running app: Boxaz clones ``repo``, runs ``install`` once, starts
        ``start``, restarts it on crash (``restart`` = always | on-failure | never), starts it again after
        wake / restore / clone and gates gateway traffic to ``port`` until it is ready. ``port`` is also the box's
        primary URL (``https://<name>.<domain>/``); ``label`` names it for ``<name>-<label>``. Returns ``{spec, status}``."""
        ready = {"http": ready_http} if ready_http else ({"tcp": True} if ready_tcp else None)
        return self._call("PUT", f"/boxes/{_q(box_id)}/app", _clean({
            "start": start, "repo": repo, "ref": ref, "install": install, "port": port, "ready": ready,
            "restart": restart, "env": dict(env) if env else None, "workdir": workdir, "backoff_max_s": backoff_max_s,
            "label": label}))

    def app(self, box_id: str) -> Dict[str, Any]:
        """App spec + live status: state (cloning|installing|starting|ready|backoff|exited|stopped|sleeping),
        ready, restarts, last_exit, started_at, supervisor (boxd | script)."""
        return self._call("GET", f"/boxes/{_q(box_id)}/app")

    def restart_app(self, box_id: str) -> Dict[str, Any]:
        return self._call("POST", f"/boxes/{_q(box_id)}/app/restart", {})

    def delete_app(self, box_id: str) -> Dict[str, Any]:
        return self._call("DELETE", f"/boxes/{_q(box_id)}/app")

    def app_logs(self, box_id: str, offset: int = -1, max_bytes: int = 16384) -> Dict[str, Any]:
        """App output ``{data, offset, truncated}``; pass the returned ``offset`` to read only new output."""
        return self._call("GET", f"/boxes/{_q(box_id)}/app/logs", params={"offset": offset, "max": max_bytes})

    def ports(self, box_id: str) -> Dict[str, Any]:
        """Every exposed port with its public ``url``, ``kind``, ``access``, stable ``gateway_url``
        (v1.9 readable: ``https://<name>.<domain>/`` for the primary port, ``https://<name>-<label>.<domain>/``
        for others), ``label``, ``primary`` and the legacy ``alias_url``; plus the box ``name`` and ``primary_port``."""
        return self._call("GET", f"/boxes/{_q(box_id)}/ports")

    def expose(self, box_id: str, port: int, label: Optional[str] = None) -> Dict[str, Any]:
        """Expose a port; ``label`` (e.g. "api") names it in its URL ``<name>-<label>.<domain>``."""
        return self._call("POST", f"/boxes/{_q(box_id)}/ports", _clean({"port": port, "label": label}))

    def create_url_token(self, box_id: str, port: int, *, ttl_s: Optional[int] = None, label: Optional[str] = None,
                         private: Optional[bool] = None) -> Dict[str, Any]:
        """Issue a scoped, expiring, revocable token for one box port. The response carries ``token``
        (shown once) and ``url`` (gateway URL + ``?token=``); ``private`` (default True) makes the port
        require a token."""
        return self._call("POST", f"/boxes/{_q(box_id)}/ports/{int(port)}/tokens", _clean({"ttl_s": ttl_s, "label": label, "private": private}))

    def url_tokens(self, box_id: str, port: int) -> Dict[str, Any]:
        return self._call("GET", f"/boxes/{_q(box_id)}/ports/{int(port)}/tokens")

    def revoke_url_token(self, box_id: str, port: int, token_id: str) -> Dict[str, Any]:
        return self._call("DELETE", f"/boxes/{_q(box_id)}/ports/{int(port)}/tokens/{_q(token_id)}")

    def set_port_access(self, box_id: str, port: int, access: str) -> Dict[str, Any]:
        """``access`` = "public" (gateway URL works without a token) or "token"."""
        return self._call("PUT", f"/boxes/{_q(box_id)}/ports/{int(port)}", {"access": access})

    def set_port_label(self, box_id: str, port: int, label: str) -> Dict[str, Any]:
        """v1.9: name a port in its gateway URL (``https://<name>-<label>.<domain>/``); returns the port view."""
        return self._call("PUT", f"/boxes/{_q(box_id)}/ports/{int(port)}", {"label": label})

    def rename_url(self, box_id: str, name: str) -> Box:
        """v1.9: set the box's readable gateway name (``https://<name>.<domain>/``). Taken or reserved names
        raise an error whose ``next`` suggests a free one; the old name stops answering, the alias keeps working."""
        return self._call("PATCH", f"/boxes/{_q(box_id)}", {"subdomain": name})

    def set_wake_on_request(self, box_id: str, on: bool) -> Box:
        """Whether a request to the box's gateway URL wakes it while it sleeps (default on)."""
        return self._call("PATCH", f"/boxes/{_q(box_id)}", {"wake_on_request": bool(on)})

    def capabilities(self) -> Dict[str, Any]:
        """Lifecycle flags per provider: memory_pause, wake_on_http_native, wake_via_gateway, fork,
        inbound_http, max_runtime, private_preview."""
        return self._call("GET", "/capabilities")

    def gateway(self, check: bool = False) -> Dict[str, Any]:
        """Gateway domain, box URL pattern (v1.9 https://<name>.<domain>/, label_pattern, alias_pattern), reserved
        subdomains and provider-preview policy; ``check=True`` probes the public URL."""
        return self._call("GET", "/gateway" + ("?check=1" if check else ""))

    # -- v1.10 access: tickets, TCP forward, SSH gateway, durable terminal ------
    def access(self, box_id: str) -> Dict[str, Any]:
        """How to reach the box beyond HTTP: ``commands`` (box forward / box ssh / ssh-config), the
        WebSocket endpoints (``tcp.url``, ``terminal.url``, ``ssh.url``), allowed TCP ports, terminal
        sessions and the SSH gateway host key (``ssh.known_hosts``)."""
        return self._call("GET", f"/boxes/{_q(box_id)}/access")

    def ticket(self, box_id: str, purpose: str, *, port: Optional[int] = None, ttl_s: Optional[int] = None,
               session_id: Optional[str] = None, allow: Optional[bool] = None) -> Dict[str, Any]:
        """Single-use access ticket (``purpose`` = "terminal" | "tcp" | "ssh"). ``ticket`` (bxa_…) is shown
        once with ``url`` = the WebSocket to open (``…&ticket=``); redeem it before ``expires_at``
        (default 60 s). tcp needs ``port``; ``allow=True`` adds it to the forward allowlist."""
        return self._call("POST", f"/boxes/{_q(box_id)}/tickets", _clean({"purpose": purpose, "port": port, "ttl_s": ttl_s,
                                                                          "session_id": session_id, "allow": allow}))

    def tickets(self, box_id: str) -> Dict[str, Any]:
        return self._call("GET", f"/boxes/{_q(box_id)}/tickets")

    def revoke_ticket(self, box_id: str, ticket_id: str = "all") -> Dict[str, Any]:
        """Revoke one ticket (or "all"); sessions opened with it close within a second."""
        return self._call("DELETE", f"/boxes/{_q(box_id)}/tickets/{_q(ticket_id)}")

    def allow_forward_port(self, box_id: str, port: int) -> Dict[str, Any]:
        """Add an in-box port to the TCP forward allowlist (exposed ports and 22 always are)."""
        return self._call("POST", f"/boxes/{_q(box_id)}/forward-ports", {"port": int(port)})

    def terminals(self, box_id: str) -> Dict[str, Any]:
        """Durable terminal sessions of the box (``items``: id, viewers, cols, rows, expires_at…)."""
        return self._call("GET", f"/boxes/{_q(box_id)}/terminals")

    def kill_terminal(self, box_id: str, session_id: str = "all") -> Dict[str, Any]:
        return self._call("DELETE", f"/boxes/{_q(box_id)}/terminals/{_q(session_id)}")

    def ssh_info(self) -> Dict[str, Any]:
        """SSH gateway host key (known_hosts line + fingerprint), direct listener, ProxyCommand."""
        return self._call("GET", "/ssh")

    def ssh_keys(self) -> Dict[str, Any]:
        return self._call("GET", "/ssh/keys")

    def add_ssh_key(self, public_key: str, name: Optional[str] = None) -> Dict[str, Any]:
        """Register a public key for the direct SSH listener (``ssh <box-name>@ssh.<domain>``)."""
        return self._call("POST", "/ssh/keys", _clean({"public_key": public_key, "name": name}))

    def delete_ssh_key(self, key_id: str) -> Dict[str, Any]:
        return self._call("DELETE", f"/ssh/keys/{_q(key_id)}")

    # -- v1.11 access P1: SSH certificates, browser (CDP), desktop (VNC) ------------
    def ssh_cert(self, box_id: str, public_key: str, ttl_s: Optional[int] = None) -> Dict[str, Any]:
        """Short-lived SSH user certificate for one box (``ttl_s`` ≤ 43200). Save ``certificate`` as
        ``<key>-cert.pub``; it authenticates without an API key (``ProxyCommand box ssh-proxy --anon %h``
        or the direct listener). Revoke with ``revoke_ticket(box_id, cert["id"])``."""
        return self._call("POST", "/ssh/certs", _clean({"box": box_id, "public_key": public_key, "ttl_s": ttl_s}))

    def browser(self, box_id: str, *, ttl_s: Optional[int] = None, headless: Optional[bool] = None) -> Dict[str, Any]:
        """Start (or reuse) Chromium in the box; returns ``cdp_url`` for Playwright
        ``chromium.connect_over_cdp(cdp_url)`` / puppeteer ``connect({browserWSEndpoint})``
        (reusable until ``expires_at``, default 1 h), ``http_url``, ``version``, ``started``, ``start_ms``."""
        return self._call("POST", f"/boxes/{_q(box_id)}/browser", _clean({"ttl_s": ttl_s, "headless": headless}))

    def cdp_url(self, box_id: str, ttl_s: Optional[int] = None) -> str:
        """Just the CDP WebSocket URL of the box's browser (starts it when needed)."""
        r = self.browser(box_id, ttl_s=ttl_s)
        if inspect.isawaitable(r):
            async def _u() -> str:
                return (await r)["cdp_url"]
            return _u()  # type: ignore[return-value]
        return r["cdp_url"]

    def connect_playwright(self, playwright: Any, box_id: str, ttl_s: Optional[int] = None) -> Any:
        """``browser = bx.connect_playwright(p, box_id)`` with ``sync_playwright()`` (or await it with
        the async API): Playwright's Chromium attached to the box's browser over CDP."""
        u = self.cdp_url(box_id, ttl_s)
        if inspect.isawaitable(u):
            async def _c() -> Any:
                return await playwright.chromium.connect_over_cdp(await u)
            return _c()
        return playwright.chromium.connect_over_cdp(u)

    def browser_status(self, box_id: str) -> Dict[str, Any]:
        return self._call("GET", f"/boxes/{_q(box_id)}/browser")

    def stop_browser(self, box_id: str) -> Dict[str, Any]:
        return self._call("DELETE", f"/boxes/{_q(box_id)}/browser")

    def desktop(self, box_id: str, *, width: Optional[int] = None, height: Optional[int] = None,
                view_only: Optional[bool] = None, ttl_s: Optional[int] = None) -> Dict[str, Any]:
        """Start the desktop (Xvfb + window manager + x11vnc) and get a single-use ``viewer_url``
        (noVNC page) and ``vnc_url`` (RFB over WebSocket)."""
        return self._call("POST", f"/boxes/{_q(box_id)}/desktop", _clean({"width": width, "height": height, "view_only": view_only, "ttl_s": ttl_s}))

    def desktop_status(self, box_id: str) -> Dict[str, Any]:
        return self._call("GET", f"/boxes/{_q(box_id)}/desktop")

    def stop_desktop(self, box_id: str) -> Dict[str, Any]:
        return self._call("DELETE", f"/boxes/{_q(box_id)}/desktop")

    def share_desktop(self, box_id: str, *, ttl_s: int = 3600, view_only: bool = True) -> Dict[str, Any]:
        """View-only (default) share link ``url`` (iframe-able, TTL 60 s … 24 h, reusable until revoked)."""
        return self._call("POST", f"/boxes/{_q(box_id)}/desktop/shares", {"ttl_s": ttl_s, "view_only": view_only})

    def desktop_shares(self, box_id: str) -> Dict[str, Any]:
        return self._call("GET", f"/boxes/{_q(box_id)}/desktop/shares")

    def revoke_desktop_shares(self, box_id: str, share_id: str = "all") -> Dict[str, Any]:
        """Revoke one share link (or "all"): viewers are disconnected, the link answers 410."""
        return self._call("DELETE", f"/boxes/{_q(box_id)}/desktop/shares/{_q(share_id)}")

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
            raise BoxazError(400, "INVALID", resp["error"].get("message", "MCP error"))
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
            raise BoxazError(400, e.get("code", "INVALID"), e.get("message", ""), e.get("hint", ""),
                            e.get("retry_after_s"), e.get("docs_url"), out)
        return out


def _parse(status: int, ctype: str, body: bytes, raw: bool) -> Any:
    if status >= 300:
        raise _error_from(status, body)
    if raw or "json" not in ctype:
        return body.decode("utf-8", errors="replace")
    return _json.loads(body.decode() or "null")


def _retry_wait(err: BoxazError, attempt: int) -> float:
    if err.retry_after_s:
        return min(float(err.retry_after_s), 30.0)
    return min(0.5 * (2 ** attempt), 8.0)


class Boxaz(_Base):
    """Synchronous client.

    >>> bx = Boxaz()                       # BOXAZ_URL / BOXAZ_API_KEY / BOXAZ_LANG from env
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

    def __enter__(self) -> "Boxaz":
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
            except BoxazError as e:
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
        """Execute a Boxaz tool by name (as returned by :func:`as_openai_tools`) through the MCP
        endpoint, so arguments are mapped exactly like the MCP server does."""
        resp = self._call("POST", "/mcp", self._mcp_body(name, arguments or {}))
        return self._mcp_result(resp)


class AsyncBoxaz(_Base):
    """Asynchronous client (httpx.AsyncClient, or urllib in a worker thread)."""

    def __init__(self, *args: Any, **kwargs: Any):
        super().__init__(*args, **kwargs)
        self._http = httpx.AsyncClient(timeout=self.timeout, trust_env=not self._loopback) if httpx is not None else None
        self._sync = None if httpx is not None else Boxaz(*args, **kwargs)

    async def aclose(self) -> None:
        if self._http is not None:
            await self._http.aclose()

    async def __aenter__(self) -> "AsyncBoxaz":
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
            except BoxazError as e:
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
