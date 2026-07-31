"""Meridian platform SDK (hand-written, generate-free).

Typed httpx/pydantic clients for the highest-value services first:
onboarding, tin-graph, ledger (see api/*.yaml in this repo).
"""

from .client import Client, MeridianError, new_idempotency_key
from .onboarding import (
    Agent,
    DocRef,
    OnboardingClient,
    OnboardingStatus,
    Operator,
    OperatorCreate,
    PresignResult,
    WorkflowRun,
)
from .tingraph import (
    CompanyProfile,
    Director,
    Entity,
    PersonRef,
    ProvisionResult,
    Shareholder,
    TinGraphClient,
    UBO,
)
from .ledger import Account, Balance, LedgerClient, Transfer

__all__ = [
    "Client", "MeridianError", "new_idempotency_key",
    "OnboardingClient", "Operator", "OperatorCreate", "WorkflowRun",
    "OnboardingStatus", "Agent", "DocRef", "PresignResult",
    "TinGraphClient", "Entity", "CompanyProfile", "PersonRef", "Director",
    "Shareholder", "UBO", "ProvisionResult",
    "LedgerClient", "Account", "Balance", "Transfer",
]
