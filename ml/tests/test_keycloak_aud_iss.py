"""A1-07 regression: ml/serving keycloak mode must validate audience AND
issuer, and PROFILE=prod must refuse to boot without KEYCLOAK_AUDIENCE /
KEYCLOAK_ISSUER. Pre-fix: jwt.decode(..., verify_aud=False) with no issuer
check accepted any token signed by any key in the realm JWKS."""
import importlib
import os
import sys
import time

import pytest

jwt = pytest.importorskip("jwt")
pytest.importorskip("fastapi")

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import serving.main as sm  # noqa: E402


@pytest.fixture()
def rsa_keypair():
    from cryptography.hazmat.primitives.asymmetric import rsa

    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    return key


def _mint(key, aud, iss):
    return jwt.encode(
        {"sub": "svc", "aud": aud, "iss": iss, "exp": int(time.time()) + 300,
         "roles": ["service"]},
        key, algorithm="RS256",
    )


class _FakeJWKS:
    def __init__(self, key):
        self._key = key

    def get_signing_key_from_jwt(self, token):
        class _K:
            key = self._key.public_key()

        return _K()


def _patched(monkeypatch, sm_mod, key):
    monkeypatch.setattr(sm_mod, "_jwks_client", lambda url: _FakeJWKS(key))
    monkeypatch.setenv("KEYCLOAK_JWKS_URL", "https://idp.example/realms/m/protocol/openid-connect/certs")


def test_prod_keycloak_requires_audience_and_issuer():
    saved = os.environ.copy()
    try:
        for extra in ({}, {"KEYCLOAK_AUDIENCE": "ml-serving"}, {"KEYCLOAK_ISSUER": "https://idp.example/realms/m"}):
            os.environ.clear()
            os.environ.update({"PROFILE": "prod", "AUTH_MODE": "keycloak", **extra})
            # boot refusal surfaces at module import (module-level
            # app = create_app()) — that IS the startup gate firing.
            with pytest.raises(RuntimeError):
                importlib.reload(sm)
        # fully configured prod boots past the gate (registry may 503 per route)
        os.environ.clear()
        os.environ.update({
            "PROFILE": "prod", "AUTH_MODE": "keycloak",
            "KEYCLOAK_AUDIENCE": "ml-serving",
            "KEYCLOAK_ISSUER": "https://idp.example/realms/m",
        })
        importlib.reload(sm)
        sm.create_app("/nonexistent-registry")
    finally:
        os.environ.clear()
        os.environ.update(saved)
        importlib.reload(sm)


def test_verify_keycloak_validates_aud_and_iss(monkeypatch, rsa_keypair):
    monkeypatch.setenv("AUTH_MODE", "keycloak")
    monkeypatch.setenv("KEYCLOAK_AUDIENCE", "ml-serving")
    monkeypatch.setenv("KEYCLOAK_ISSUER", "https://idp.example/realms/m")
    importlib.reload(sm)
    _patched(monkeypatch, sm, rsa_keypair)
    try:
        good = _mint(rsa_keypair, "ml-serving", "https://idp.example/realms/m")
        assert sm._verify_keycloak(good) is not None

        # token minted for a DIFFERENT client of the same realm -> rejected
        wrong_aud = _mint(rsa_keypair, "some-other-client", "https://idp.example/realms/m")
        assert sm._verify_keycloak(wrong_aud) is None

        # same key, different issuer -> rejected
        wrong_iss = _mint(rsa_keypair, "ml-serving", "https://evil.example/realms/x")
        assert sm._verify_keycloak(wrong_iss) is None
    finally:
        monkeypatch.undo()
        importlib.reload(sm)


def test_verify_keycloak_denies_when_audience_unconfigured(monkeypatch, rsa_keypair):
    """No audience configured = nothing to bind to; fail closed (pre-fix
    decoded with verify_aud=False and accepted the token)."""
    monkeypatch.setenv("AUTH_MODE", "keycloak")
    monkeypatch.delenv("KEYCLOAK_AUDIENCE", raising=False)
    monkeypatch.delenv("KEYCLOAK_ISSUER", raising=False)
    importlib.reload(sm)
    _patched(monkeypatch, sm, rsa_keypair)
    try:
        tok = _mint(rsa_keypair, "anything", "https://idp.example/realms/m")
        assert sm._verify_keycloak(tok) is None
    finally:
        monkeypatch.undo()
        importlib.reload(sm)
