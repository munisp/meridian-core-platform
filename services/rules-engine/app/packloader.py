"""Pack loading: local PACKS_DIR (packs/<id>/<version>.yaml) and/or the
rp-registry service (RP_REGISTRY_URL). Packs are validated on load."""
from __future__ import annotations

import os
import threading
from pathlib import Path
from typing import Any

import yaml


class PackNotFound(KeyError):
    pass


class PackLoader:
    def __init__(self, packs_dir: str | None = None, registry_url: str | None = None) -> None:
        self.packs_dir = Path(packs_dir or os.environ.get("PACKS_DIR", "packs"))
        self.registry_url = (registry_url or os.environ.get("RP_REGISTRY_URL", "")).rstrip("/")
        self._lock = threading.RLock()
        self._cache: dict[str, dict] = {}
        self._mtime: dict[str, float] = {}

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
        with self._lock:
            self._cache[k] = pack
            self._mtime[k] = mtime
        return pack

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
