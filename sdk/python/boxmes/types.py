"""Typed shapes of the Boxmes API (docs/api-contract-v1.md, v1.1).

All are ``TypedDict`` with ``total=False`` so new server fields never break
clients; required contract fields are documented on each class.
"""

from __future__ import annotations

from typing import Any, Dict, List, Literal, Optional, TypedDict, Union

Lifecycle = Literal["task", "session", "service"]
BoxStatus = Literal["requested", "placing", "provisioning", "running", "sleeping", "snapshotting", "terminated", "failed"]
ToolFormat = Literal["openai", "anthropic", "gemini", "mcp"]
Locale = Literal["en", "vi", "zh", "ja"]


class Spec(TypedDict, total=False):
    cpu: float
    ram_mb: int
    disk_gb: int


class Box(TypedDict, total=False):
    id: str
    slug: str
    agent_id: str
    agent_name: str
    status: BoxStatus
    lifecycle: Lifecycle
    template: str
    spec: Spec
    provider: str
    region: str
    host_id: str
    url: str
    ttl_seconds: int
    idle_minutes: int
    budget_usd: float
    spent_usd: float
    cost_per_hour: float
    placement_reason: str
    ports: List[int]
    secrets: List[str]
    created_at: str
    last_active_at: str
    forked_from: str
    locale: str
    error: str
    next: List[str]
    subdomain: str
    port_labels: Dict[str, str]
    workspace_id: str  # v2.0
    location: "BoxLocation"  # v2.0, null until placed


class ExecResult(TypedDict, total=False):
    exit_code: int
    duration_ms: int
    stdout_tail: str
    stderr_tail: str
    truncated: bool
    log_id: Optional[str]
    files_changed: List[str]
    tokens_saved: int
    next: List[str]


class JobRef(TypedDict, total=False):
    job_id: str
    next: List[str]


class Job(TypedDict, total=False):
    id: str
    box_id: str
    command: str
    status: Literal["queued", "running", "done", "failed"]
    result: Optional[ExecResult]
    created_at: str
    finished_at: Optional[str]


class BatchItem(ExecResult, total=False):
    index: int
    command: str


class BatchResult(TypedDict, total=False):
    ok: bool
    results: List[BatchItem]
    completed: int
    stopped_at: Optional[int]
    duration_ms: int
    tokens_saved: int
    next: List[str]


class QuoteOption(TypedDict, total=False):
    kind: Literal["pool", "burst"]
    provider: str
    region: str
    host_id: str
    sku: str
    cost_per_hour: float
    expected_total_usd: float
    requires_approval: bool
    label: str


class Quote(TypedDict, total=False):
    available: bool
    provider: str
    region: str
    host_id: str
    billing: str
    cost_per_hour: float
    expected_hours: float
    expected_total_usd: float
    placement_reason: str
    alternatives: List[QuoteOption]
    spec: Spec
    lifecycle: Lifecycle
    template: str
    within_budget: bool
    budget_error: Dict[str, Any]
    next: List[str]


class Snapshot(TypedDict, total=False):
    id: str
    box_id: str
    size_mb: float
    created_at: str
    location: str


class Template(TypedDict, total=False):
    name: str
    description: str
    yaml: str
    uses: int
    created_at: str
    builtin: bool


class AgentKind(TypedDict, total=False):
    id: str
    name: str
    category: Literal["coding", "framework", "workflow", "platform", "custom"]
    transports: List[str]
    doc_url: Optional[str]
    language: str


class Snippet(TypedDict, total=False):
    kind: str
    language: str
    snippet: str
    file: str
    notes: List[str]
    locale: str


class Event(TypedDict, total=False):
    id: str
    ts: str
    actor: str
    box_id: Optional[str]
    agent_id: Optional[str]
    type: str
    message: str
    cost_usd: Optional[float]
    data: Dict[str, Any]


class Agent(TypedDict, total=False):
    id: str
    name: str
    kind: str
    budget_day_usd: float
    budget_month_usd: float
    spent_today_usd: float
    spent_month_usd: float
    box_count: int
    locale: str
    created_at: str


class ApiKey(TypedDict, total=False):
    id: str
    agent_id: Optional[str]
    name: str
    prefix: str
    scopes: List[str]
    key: str
    created_at: str
    # v2.0
    workspace_id: str
    kind: Literal["platform", "workspace", "agent"]
    expires_at: Optional[str]
    created_by: str
    last_used_at: Optional[str]
    revoked_at: Optional[str]


# -- v2.0 tenancy ---------------------------------------------------------------
Role = Literal["owner", "admin", "developer", "billing", "viewer", "auditor"]
Plan = Literal["free", "pro", "team", "unlimited"]


class Quota(TypedDict, total=False):
    """Effective workspace limits; 0 means unlimited."""
    plan: str
    max_boxes: int
    max_cpu_per_box: float
    max_ram_mb_per_box: int
    max_ttl_hours: int
    public_url: bool
    egress: Literal["restricted", "open"]
    max_members: int
    max_agents: int
    max_keys: int
    max_secrets: int
    max_boxes_per_minute: int


class Workspace(TypedDict, total=False):
    id: str
    name: str
    owner_user_id: str
    plan: str
    quota_overrides: Dict[str, Any]
    status: Literal["active", "suspended"]
    created_at: str
    created_by: str
    role: str  # caller's role (GET /workspaces, /workspaces/current)
    quota: Quota  # /workspaces/current
    permissions: List[str]
    member_count: int


class Me(TypedDict, total=False):
    user: Dict[str, Any]
    workspace: Optional[Workspace]
    workspaces: List[Workspace]
    role: str
    permissions: List[str]
    platform_admin: bool


class UsageCounts(TypedDict, total=False):
    boxes: int
    boxes_total: int
    agents: int
    keys: int
    secrets: int
    members: int
    spent_today_usd: float
    spent_month_usd: float


class Usage(TypedDict, total=False):
    workspace_id: str
    plan: str
    quota: Quota
    used: UsageCounts


class Member(TypedDict, total=False):
    user_id: str
    email: str
    name: str
    role: str
    email_verified: bool
    joined_at: str


class WorkspaceInvite(TypedDict, total=False):
    id: str
    workspace_id: str
    email: str
    role: str
    invited_by: str
    expires_at: str
    accepted_at: Optional[str]
    accepted_by: str
    created_at: str
    status: Literal["pending", "accepted", "expired"]
    invite_url: str  # create response only
    mailed: bool  # create response only


class InvitePreview(TypedDict, total=False):
    workspace: Dict[str, str]
    email: str
    role: str
    invited_by: str
    expires_at: str
    status: Literal["pending", "accepted", "expired"]
    account_exists: bool


class BoxLocation(TypedDict, total=False):
    region: Literal["eu", "us", "apac", "sandbox", "local", "other"]
    country: str
    city: str
    provider: str
    site: str
    host: str


class LocationRow(BoxLocation, total=False):
    running: int
    sleeping: int
    total: int
    cost_today_usd: float


class RegionRow(TypedDict, total=False):
    region: str
    running: int
    sleeping: int
    total: int
    cost_today_usd: float


class LocationsView(TypedDict, total=False):
    items: List[LocationRow]
    regions: List[RegionRow]
    total: RegionRow


BatchCommand = Union[str, Dict[str, Any]]

__all__ = [
    "Agent", "AgentKind", "ApiKey", "BatchCommand", "BatchItem", "BatchResult", "Box", "BoxStatus", "Event",
    "ExecResult", "Job", "JobRef", "Lifecycle", "Locale", "Quote", "QuoteOption", "Snapshot", "Snippet", "Spec",
    "Template", "ToolFormat",
    # v2.0 tenancy
    "BoxLocation", "InvitePreview", "LocationRow", "LocationsView", "Me", "Member", "Plan", "Quota", "RegionRow", "Role",
    "Usage", "UsageCounts", "Workspace", "WorkspaceInvite",
]
