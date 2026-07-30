"""Keycloak RS256 verification tests (HARDENING H2 python mirror)."""
import os
import sys
import time
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

jwt = pytest.importorskip("jwt")
from cryptography.hazmat.primitives.asymmetric import rsa  # noqa: E402

from meridian_events import auth  # noqa: E402

ISS = "https://keycloak:8443/realms/meridian"
AUD = "meridian-services"


@pytest.fixture()
def rsa_keys():
    return rsa.generate_private_key(public_exponent=65537, key_size=2048)


def _token(priv, **over):
    claims = {
        "sub": "svc-rules",
        "iss": ISS,
        "aud": AUD,
        "exp": int(time.time()) + 3600,
        "iat": int(time.time()),
        "realm_access": {"roles": ["operator", "auditor", "operator"]},
    }
    claims.update(over)
    return jwt.encode(claims, priv, algorithm="RS256", headers={"kid": "k1"})


@pytest.fixture(autouse=True)
def env(monkeypatch, rsa_keys):
    monkeypatch.setenv("AUTH_MODE", "keycloak")
    monkeypatch.setenv("KEYCLOAK_ISSUER", ISS)
    monkeypatch.setenv("KEYCLOAK_AUDIENCE", AUD)

    class FakeJWK:
        def get_signing_key_from_jwt(self, token):
            class K:
                key = rsa_keys.public_key()
            return K()

    monkeypatch.setattr(auth, "_KEYCLOAK_JWKS_CLIENT", FakeJWK())
    yield
    monkeypatch.setattr(auth, "_KEYCLOAK_JWKS_CLIENT", None)


def test_verify_keycloak_maps_realm_roles(rsa_keys):
    claims = auth.verify_keycloak(_token(rsa_keys))
    assert claims.sub == "svc-rules"
    assert claims.roles == ["auditor", "operator"]  # deduped + sorted


def test_verify_bearer_selects_keycloak(rsa_keys):
    assert auth._verify_bearer(_token(rsa_keys)).sub == "svc-rules"


def test_expired_rejected(rsa_keys):
    with pytest.raises(auth.AuthError):
        auth.verify_keycloak(_token(rsa_keys, exp=int(time.time()) - 10))


def test_wrong_issuer_rejected(rsa_keys):
    with pytest.raises(auth.AuthError):
        auth.verify_keycloak(_token(rsa_keys, iss="https://evil/realms/x"))


def test_wrong_audience_rejected(rsa_keys):
    with pytest.raises(auth.AuthError):
        auth.verify_keycloak(_token(rsa_keys, aud="other"))


def test_hs256_token_rejected_in_keycloak_mode():
    hs = auth.sign_hs256(auth.Claims(sub="x"), 60)
    with pytest.raises(auth.AuthError):
        auth.verify_keycloak(hs)


def test_dev_mode_unchanged(monkeypatch):
    monkeypatch.setenv("AUTH_MODE", "dev")
    tok = auth.sign_hs256(auth.Claims(sub="dev-user", roles=["admin"]), 60)
    assert auth._verify_bearer(tok).sub == "dev-user"
