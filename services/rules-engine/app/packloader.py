"""Pack loading: local PACKS_DIR (packs/<id>/<version>.yaml) and/or the
rp-registry service (RP_REGISTRY_URL). Packs are validated on load.

Integrity (PACK_INTEGRITY=enforce|off, default: enforce when a lockfile is
found): packs pinned in a packs.lock.json (PACKS_LOCK_PATH) must match their
canonical sha256 pin; published packs must carry an ed25519 ceremony
signature that verifies against rule-packs/signing_keys.json
(PACK_SIGNING_KEYS). Unsigned or hash-mismatched published packs are
rejected when enforcing."""
from __future__ import annotations

import hashlib
import json
import os
import threading
from pathlib import Path
from typing import Any

import yaml


class PackNotFound(KeyError):
    pass


class PackIntegrityError(ValueError):
    """Hash pin mismatch, missing/invalid signature, or unsigned published
    pack while integrity enforcement is active."""


def canonical_pack_bytes(pack: dict) -> bytes:
    """Canonical bytes for hashing/signing: the pack mapping without the
    `signed` block, JSON-dumped with sorted keys."""
    p = {k: v for k, v in pack.items() if k != "signed"}
    return json.dumps(p, sort_keys=True, separators=(",", ":"), default=str).encode()


def _load_lock(lock_path: Path) -> dict:
    try:
        return json.loads(lock_path.read_text()).get("pins", {})
    except (OSError, json.JSONDecodeError):
        return {}


def _load_signing_keys(keys_path: Path) -> dict:
    try:
        return json.loads(keys_path.read_text()).get("keys", {})
    except (OSError, json.JSONDecodeError):
        return {}


def _verify_ed25519(sig_hex: str, msg: bytes, pub_hex: str) -> bool:
    try:
        from ed25519_verify import verify  # vendored beside this file
    except ImportError:
        try:
            import sys

            sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "packages" / "rulepack-schema"))
            from ed25519_verify import verify
        except ImportError:
            return False
    try:
        return verify(bytes.fromhex(sig_hex), msg, bytes.fromhex(pub_hex))
    except Exception:  # noqa: BLE001
        return False


# Anchor default paths to the service directory (this file lives at
# services/rules-engine/app/packloader.py) so pack/lock/keys resolution is
# independent of the process working directory (e.g. pytest run from the
# repo root must resolve the same paths as one run from the service dir).
# The repo-root rule-packs/ lockfile intentionally is NOT a default
# candidate: it pins the canonical consumer packs, and enforcing it against
# the unsigned dev seed packs in services/rules-engine/packs would reject
# them. Consumers of the canonical packs pass lock_path/signing_keys_path
# explicitly (see tests/test_vat_packs.py).
_SERVICE_DIR = Path(__file__).resolve().parents[1]


def _resolve_under_service(path: str) -> Path:
    """Relative paths are interpreted against the service directory, not cwd."""
    p = Path(path)
    return p if p.is_absolute() else _SERVICE_DIR / p


class PackLoader:
    def __init__(self, packs_dir: str | None = None, registry_url: str | None = None,
                 lock_path: str | None = None, signing_keys_path: str | None = None,
                 enforce: bool | None = None) -> None:
        self.packs_dir = _resolve_under_service(
            packs_dir or os.environ.get("PACKS_DIR", "packs"))
        self.registry_url = (registry_url or os.environ.get("RP_REGISTRY_URL", "")).rstrip("/")
        self._lock = threading.RLock()
        self._cache: dict[str, dict] = {}
        self._mtime: dict[str, float] = {}
        lock_env = os.environ.get("PACKS_LOCK_PATH", "")
        keys_env = os.environ.get("PACK_SIGNING_KEYS", "")
        candidates = [lock_env] if lock_env else [
            str(self.packs_dir / "packs.lock.json"),
            str(self.packs_dir.parent / "packs.lock.json"),
        ]
        self._pins: dict[str, dict] = {}
        for c in candidates:
            if c and Path(c).exists():
                self._pins = _load_lock(Path(c))
                break
        key_candidates = [keys_env] if keys_env else [
            str(self.packs_dir / "signing_keys.json"),
            str(self.packs_dir.parent / "signing_keys.json"),
        ]
        self._signing_keys: dict[str, dict] = {}
        for c in key_candidates:
            if c and Path(c).exists():
                self._signing_keys = _load_signing_keys(Path(c))
                break
        if enforce is None:
            enforce = os.environ.get("PACK_INTEGRITY", "").lower() == "enforce" or bool(self._pins)
        self.enforce = enforce

    @staticmethod
    def key(pack_id: str, version: str | None) -> str:
        return f"{pack_id}@{version or 'latest'}"

    def load_local(self, pack_id: str, version: str) -> dict | None:
        path = self.packs_dir / pack_id / f"{version}.yaml"
        if not path.exists():
            return None
        mtime = path.stat().st_mtime
        k = self.key(pack_id, version)
        with self._lock:
            if k in self._cache and self._mtime.get(k) == mtime:
                return self._cache[k]
        pack = yaml.safe_load(path.read_text())
        if isinstance(pack, dict):
            self._verify_integrity(pack_id, str(version), pack)
        with self._lock:
            self._cache[k] = pack
            self._mtime[k] = mtime
        return pack

    def _verify_integrity(self, pack_id: str, version: str, pack: dict) -> None:
        """Enforce sha256 pins + ceremony signatures on published packs."""
        if not self.enforce:
            return
        pin = (self._pins.get(pack_id) or {}).get("sha256")
        digest = hashlib.sha256(canonical_pack_bytes(pack)).hexdigest()
        if pin and pin != digest:
            raise PackIntegrityError(
                f"{pack_id}@{version}: sha256 pin mismatch "
                f"(lock={pin[:16]}… actual={digest[:16]}…)")
        if pack.get("status") != "published":
            return  # simulation/draft packs may be unsigned
        signed = pack.get("signed")
        if not signed:
            raise PackIntegrityError(
                f"{pack_id}@{version}: published pack is unsigned")
        if signed.get("algorithm") != "ed25519":
            raise PackIntegrityError(f"{pack_id}@{version}: unsupported signature algorithm")
        key = self._signing_keys.get(signed.get("key_id", ""))
        if not key:
            raise PackIntegrityError(
                f"{pack_id}@{version}: unknown signing key_id {signed.get('key_id')!r}")
        if not _verify_ed25519(str(signed.get("signature", "")), bytes.fromhex(digest),
                               str(key.get("public_key_hex", ""))):
            raise PackIntegrityError(f"{pack_id}@{version}: signature verification failed")

    def latest_local(self, pack_id: str) -> dict | None:
        d = self.packs_dir / pack_id
        if not d.exists():
            return None
        versions = sorted(
            (p.stem for p in d.glob("*.yaml")),
            key=lambda v: [int(x) for x in v.split(".") if x.isdigit()],
        )
        for v in reversed(versions):
            pack = self.load_local(pack_id, v)
            if pack and pack.get("status") == "published":
                return pack
        return self.load_local(pack_id, versions[-1]) if versions else None

    def load_registry(self, pack_id: str, version: str | None) -> dict | None:
        if not self.registry_url:
            return None
        import httpx

        if version in (None, "latest"):
            url = f"{self.registry_url}/v1/packs/{pack_id}/latest"
        else:
            url = f"{self.registry_url}/v1/packs/{pack_id}/{version}"
        try:
            r = httpx.get(url, timeout=5.0)
        except httpx.HTTPError:
            return None
        if r.status_code != 200:
            return None
        body = r.json()
        pack = body.get("pack") if isinstance(body, dict) else None
        if pack is None and isinstance(body, dict) and "rules" in body:
            pack = body
        if pack is None and isinstance(body, dict) and body.get("yaml"):
            pack = yaml.safe_load(body["yaml"])
        return pack

    def get(self, pack_id: str, version: str | None = None) -> dict:
        pack = None
        if version in (None, "latest"):
            pack = self.latest_local(pack_id) or self.load_registry(pack_id, None)
        else:
            pack = self.load_local(pack_id, version) or self.load_registry(pack_id, version)
        if pack is None:
            raise PackNotFound(f"{pack_id}@{version or 'latest'}")
        return pack

    def get_for_date(self, pack_id: str, filing_date: str,
                     version: str | None = None) -> dict:
        """Effective-date pack selection: pick the version in force at
        filing_date (effective_from <= filing_date <= effective_to), so
        backdated filings evaluate against the law as it then stood.
        Falls back to get() when no versioned local directory exists."""
        d = self.packs_dir / pack_id
        if version in (None, "latest") and d.exists():
            versions = sorted(
                (p.stem for p in d.glob("*.yaml")),
                key=lambda v: [int(x) for x in v.split(".") if x.isdigit()],
            )
            candidates: list[dict] = []
            for v in versions:
                try:
                    pack = self.load_local(pack_id, v)
                except PackIntegrityError:
                    raise
                except Exception:  # noqa: BLE001
                    continue
                if pack:
                    candidates.append(pack)
            in_force = [
                p for p in candidates
                if (not p.get("effective_from") or str(p["effective_from"]) <= filing_date)
                and (not p.get("effective_to") or str(p["effective_to"]) >= filing_date)
            ]
            if in_force:
                published = [p for p in in_force if p.get("status") == "published"]
                pool = published or in_force
                return max(pool, key=lambda p: str(p.get("effective_from") or ""))
        return self.get(pack_id, version)

    def list_loaded(self) -> list[dict[str, Any]]:
        out: list[dict[str, Any]] = []
        if self.packs_dir.exists():
            for pack_dir in sorted(self.packs_dir.iterdir()):
                if not pack_dir.is_dir():
                    continue
                for f in sorted(pack_dir.glob("*.yaml")):
                    try:
                        pack = self.load_local(pack_dir.name, f.stem)
                    except Exception:  # noqa: BLE001
                        continue
                    if pack:
                        out.append({
                            "id": pack.get("id"), "version": str(pack.get("version")),
                            "status": pack.get("status"),
                            "rules": len(pack.get("rules", [])),
                            "source": "local",
                            "subject_to_regazette": pack.get("subject_to_regazette", False),
                        })
        # merge registry-listed packs when configured
        if self.registry_url:
            try:
                import httpx

                r = httpx.get(f"{self.registry_url}/v1/packs", timeout=5.0)
                if r.status_code == 200:
                    for p in r.json().get("packs", []):
                        entry = {
                            "id": p.get("id"), "version": p.get("version"),
                            "status": p.get("status"), "rules": p.get("rules", 0),
                            "source": "registry",
                            "subject_to_regazette": p.get("subject_to_regazette", False),
                        }
                        if entry not in out:
                            out.append(entry)
            except Exception:  # noqa: BLE001 - registry optional in dev
                pass
        return out
