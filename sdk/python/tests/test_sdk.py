"""SDK tests against an httpx MockTransport emulating the dev services."""

import httpx
import pytest

from meridian_sdk import (
    Client,
    CompanyProfile,
    MeridianError,
    OperatorCreate,
    Shareholder,
    Transfer,
    new_idempotency_key,
)


def handler(request: httpx.Request) -> httpx.Response:
    path, method = request.url.path, request.method
    if path == "/v1/operators" and method == "POST":
        assert request.headers["Idempotency-Key"].startswith("idem-")
        assert request.headers["X-Dev-Role"] == "operator"
        return httpx.Response(201, json={"id": "op_1", "full_name": "Adaeze", "status": "registered", "agent_id": "ag_1"})
    if path == "/v1/operators/op_1/status" and method == "POST":
        import json as _json

        body = _json.loads(request.content)
        if body["to"] == "graduated":
            return httpx.Response(409, json={"title": "illegal_transition", "detail": "registered -> graduated"})
        return httpx.Response(200, json={"id": "op_1", "full_name": "Adaeze", "status": body["to"]})
    if path == "/v1/tin/provision" and method == "POST":
        return httpx.Response(200, json={"id": "run_1", "workflow": "wf-onb-tin-provision", "status": "completed", "steps": []})
    if path == "/v1/onboarding/op_1":
        return httpx.Response(200, json={"operator_id": "op_1", "status": "registered",
                                         "current_step": "identity_verification",
                                         "missing_items": ["nimc_verification"]})
    if path == "/v1/tin/provision-fail":
        return httpx.Response(422, json={"title": "workflow_failed"})
    if path == "/v1/entities/e_1/kyb" and method == "POST":
        return httpx.Response(200, json={"entity": {"id": "e_1", "entity_type": "company",
                                                    "ubos": [{"name": "A Bello", "share_percent": 40, "source": "derived"}]}})
    if path == "/v1/entities/e_1/ubos":
        return httpx.Response(200, json={"entity_id": "e_1", "ubos": [{"name": "A Bello", "share_percent": 40}],
                                         "ubo_threshold_percent": 25})
    if path == "/v1/accounts/acct_1/balance":
        return httpx.Response(200, json={"account_id": "acct_1", "posted_net": 5000, "available": 4800})
    if path == "/v1/transfers" and method == "POST":
        import json as _json

        body = _json.loads(request.content)
        body["id"] = "tx_1"
        return httpx.Response(201, json=body)
    return httpx.Response(404, json={"title": "not_found"})


def make_client() -> Client:
    transport = httpx.MockTransport(handler)
    return Client("http://testserver", http=httpx.Client(transport=transport))


def test_onboarding_flow():
    c = make_client().onboarding()
    op = c.create_operator(OperatorCreate(nin="12345678901", full_name="Adaeze", agent_id="ag_1"),
                           idempotency_key=new_idempotency_key())
    assert op.id == "op_1" and op.status == "registered"

    op2 = c.transition_status("op_1", "pending_review", "outage")
    assert op2.status == "pending_review"

    with pytest.raises(MeridianError) as exc:
        c.transition_status("op_1", "graduated")
    assert exc.value.status == 409 and exc.value.title == "illegal_transition"

    run = c.provision_tin("op_1", "12345678901")
    assert run.status == "completed"

    st = c.resumption("op_1")
    assert st.current_step == "identity_verification"
    assert "nimc_verification" in st.missing_items


def test_tingraph_kyb():
    c = make_client().tingraph()
    cp = CompanyProfile(company_name="Test Ltd", rc_number="RC123456",
                        shareholders=[Shareholder(name="A Bello", share_percent=40)])
    e = c.update_kyb("e_1", cp)
    assert e.entity_type == "company" and e.ubos[0].source == "derived"
    uv = c.entity_ubos("e_1")
    assert uv.ubo_threshold_percent == 25 and len(uv.ubos) == 1


def test_ledger():
    c = make_client().ledger()
    b = c.balance("acct_1")
    assert b.posted_net == 5000 and b.available == 4800
    tr = c.transfer(Transfer(debit_account_id="a", credit_account_id="acct_1",
                             amount=100, ledger=700, code=4))
    assert tr.id == "tx_1"


def test_error_mapping():
    c = make_client()
    with pytest.raises(MeridianError) as exc:
        c.post("/v1/tin/provision-fail", {})
    assert exc.value.status == 422
