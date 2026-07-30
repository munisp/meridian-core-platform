"""Python types for envelope + nrs.*.v1 payloads (SPEC 2 packages/schemas)."""
from __future__ import annotations

from typing import Any

from pydantic import BaseModel, Field


class Envelope(BaseModel):
    id: str = Field(min_length=26, max_length=26)
    type: str
    source: str
    time: str
    tenant_id: str = ""
    trace_id: str = ""
    rule_pack_version: str = ""
    data: Any = None


class PaymentEvent(BaseModel):
    reference: str
    amount_kobo: int = Field(ge=0)
    tin_hash: str | None = None
    band: str | None = None
    state: str | None = None
    certificate_serial: str | None = None


class RulePackPublished(BaseModel):
    pack_id: str
    version: str
    ref: str
    sha256: str
    effective_from: str
    subject_to_regazette: bool = True
    published_by: str = ""


class LedgerTransferEvent(BaseModel):
    transfer_id: str
    debit_account_id: str
    credit_account_id: str
    amount_kobo: int = Field(ge=0)
    ledger: int
    code: int
    pending: bool = False


class CaseFeedEvent(BaseModel):
    case_id: str
    pseudo_tin: str
    score: float
    reasons: list[str] = Field(default_factory=list)
