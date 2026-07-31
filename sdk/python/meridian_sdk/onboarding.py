"""Onboarding service client (api/onboarding.yaml)."""

from __future__ import annotations

from typing import Any, Optional

from pydantic import BaseModel

from .client import Client


class OperatorCreate(BaseModel):
    nin: str
    full_name: str
    phone: str = ""
    state: str = ""
    lga: str = ""
    trade_category: str = ""
    agent_id: str = ""


class DocRef(BaseModel):
    id: str
    kind: str
    filename: str = ""
    object_key: str = ""
    sha256: str = ""
    size_bytes: int = 0
    status: str
    worm: bool = False


class Operator(BaseModel):
    id: str
    nin_hash: str = ""
    tin: str = ""
    tin_hash: str = ""
    full_name: str
    phone: str = ""
    state: str = ""
    lga: str = ""
    trade_category: str = ""
    status: str
    review_status: str = ""
    documents: list[DocRef] = []
    agent_id: str = ""
    created_at: str = ""
    updated_at: str = ""


class WorkflowRun(BaseModel):
    id: str
    workflow: str
    steps: list[str] = []
    status: str
    error: str = ""
    result: Any = None
    attempt: int = 1


class OnboardingStatus(BaseModel):
    operator_id: str
    status: str
    current_step: str
    missing_items: list[str] = []
    documents: list[DocRef] = []
    review_status: str = ""
    tin_hash: str = ""
    next_actions: list[str] = []


class Agent(BaseModel):
    id: str = ""
    full_name: str
    phone: str
    license_no: str = ""
    state: str = ""
    lga: str = ""
    association_id: str = ""
    vetting_status: str = ""


class PresignResult(BaseModel):
    doc_id: str
    upload_url: str
    method: str = "PUT"
    expires_at: str = ""
    backend: str = ""


class OnboardingClient:
    def __init__(self, c: Client):
        self._c = c

    def create_operator(self, op: OperatorCreate, *, idempotency_key: Optional[str] = None) -> Operator:
        return self._c.post("/v1/operators", op, model=Operator, idempotency_key=idempotency_key)

    def get_operator(self, operator_id: str) -> Operator:
        return self._c.get(f"/v1/operators/{operator_id}", model=Operator)

    def transition_status(self, operator_id: str, to: str, reason: str = "") -> Operator:
        return self._c.post(f"/v1/operators/{operator_id}/status", {"to": to, "reason": reason}, model=Operator)

    def provision_tin(self, operator_id: str, nin: str) -> WorkflowRun:
        return self._c.post("/v1/tin/provision", {"operator_id": operator_id, "nin": nin}, model=WorkflowRun)

    def redrive_run(self, run_id: str) -> WorkflowRun:
        return self._c.post(f"/v1/workflows/runs/{run_id}/redrive", model=WorkflowRun)

    def resumption(self, operator_id: str) -> OnboardingStatus:
        return self._c.get(f"/v1/onboarding/{operator_id}", model=OnboardingStatus)

    def register_agent(self, agent: Agent) -> Agent:
        return self._c.post("/v1/agents", agent, model=Agent)

    def set_agent_vetting(self, agent_id: str, status: str, notes: str = "") -> Agent:
        return self._c.post(f"/v1/agents/{agent_id}/vetting", {"status": status, "notes": notes}, model=Agent)

    def presign_document(self, operator_id: str, kind: str, filename: str = "") -> PresignResult:
        return self._c.post(f"/v1/operators/{operator_id}/documents/presign",
                            {"kind": kind, "filename": filename}, model=PresignResult)

    def complete_document(self, operator_id: str, doc_id: str, sha256: str = "", size_bytes: int = 0) -> DocRef:
        return self._c.post(f"/v1/operators/{operator_id}/documents/complete",
                            {"doc_id": doc_id, "sha256": sha256, "size_bytes": size_bytes}, model=DocRef)

    def review_queue(self) -> list[Operator]:
        out = self._c.get("/v1/review/queue")
        return [Operator.model_validate(o) for o in out.get("queue", [])]

    def review_approve(self, operator_id: str) -> Operator:
        return self._c.post(f"/v1/review/{operator_id}/approve", model=Operator)

    def review_reject(self, operator_id: str) -> Operator:
        return self._c.post(f"/v1/review/{operator_id}/reject", model=Operator)
