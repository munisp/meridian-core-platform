"""tin-graph service client (api/tin-graph.yaml)."""

from __future__ import annotations

from typing import Optional

from pydantic import BaseModel

from .client import Client


class PersonRef(BaseModel):
    name: str
    dob: str = ""
    nin_hash: str = ""
    id_doc_ref: str = ""


class Director(BaseModel):
    name: str
    dob: str = ""
    nin_hash: str = ""
    id_doc_ref: str = ""
    role: str = "director"
    share_percent: float = 0.0


class Shareholder(BaseModel):
    name: str
    dob: str = ""
    nin_hash: str = ""
    id_doc_ref: str = ""
    share_percent: float
    via_entity: str = ""


class UBO(BaseModel):
    name: str
    dob: str = ""
    nin_hash: str = ""
    id_doc_ref: str = ""
    share_percent: float = 0.0
    via_entity: str = ""
    source: str = ""


class CompanyProfile(BaseModel):
    company_name: str
    rc_number: str
    incorporation_date: str = ""
    registered_address: str = ""
    share_capital_kobo: int = 0
    status: str = ""
    directors: list[Director] = []
    shareholders: list[Shareholder] = []


class Entity(BaseModel):
    id: str
    tin: str = ""
    tin_hash: str = ""
    nin_hash: str = ""
    cac_rc: str = ""
    entity_type: str
    name: str = ""
    directors: list[Director] = []
    shareholders: list[Shareholder] = []
    ubos: list[UBO] = []
    registry_cross_check: str = ""
    created_at: str = ""


class ProvisionResult(BaseModel):
    entity: Entity
    tin: str
    tin_hash: str
    ubos: list[UBO] = []


class UBOView(BaseModel):
    entity_id: str
    entity_type: str = ""
    cac_rc: str = ""
    ubos: list[UBO] = []
    directors: list[Director] = []
    ubo_threshold_percent: float = 25.0
    registry_cross_check: str = ""


class TinGraphClient:
    def __init__(self, c: Client):
        self._c = c

    def provision_tin(
        self,
        *,
        nin: str = "",
        cac_rc: str = "",
        name: str = "",
        entity_type: str = "",
        company: Optional[CompanyProfile] = None,
    ) -> ProvisionResult:
        body: dict = {"name": name, "entity_type": entity_type}
        if nin:
            body["nin"] = nin
        if cac_rc:
            body["cac_rc"] = cac_rc
        if company:
            body["company"] = company.model_dump(mode="json", exclude_none=True)
        return self._c.post("/v1/tin/provision", body, model=ProvisionResult)

    def verify_tin(self, tin: str) -> bool:
        out = self._c.post("/v1/verify/tin", {"tin": tin})
        return bool(out.get("valid"))

    def entity_ubos(self, entity_id: str) -> UBOView:
        return self._c.get(f"/v1/entities/{entity_id}/ubos", model=UBOView)

    def update_kyb(self, entity_id: str, profile: CompanyProfile) -> Entity:
        out = self._c.post(f"/v1/entities/{entity_id}/kyb", profile)
        return Entity.model_validate(out["entity"])
