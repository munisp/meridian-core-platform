"""Scheduled trigger for continual training.

Invokes the training-side contract:
    python -m training.continual --since <window>

Ray is OPTIONAL and import-guarded:
  - If `import ray` succeeds, ray.init() is called (local mode unless
    RAY_ADDRESS points at an existing cluster). When a Ray cluster exists,
    training.continual can use Ray for distributed data-parallel training
    across workers; this trigger simply initialises the runtime and passes
    the command through.
  - Without Ray the trigger runs the same command sequentially in-process.

Scheduling: simple interval loop (ML_CONTINUAL_INTERVAL_HOURS, default 24h)
or one-shot via --once. Designed to be wrapped by cron / Temporal schedule
in production.
"""
from __future__ import annotations

import argparse
import logging
import os
import subprocess
import sys
import time

logger = logging.getLogger("ml.pipelines.continual_trigger")
logging.basicConfig(level=os.environ.get("LOG_LEVEL", "INFO"))

DEFAULT_WINDOW = os.environ.get("ML_CONTINUAL_WINDOW", "7d")
DEFAULT_INTERVAL_H = float(os.environ.get("ML_CONTINUAL_INTERVAL_HOURS", "24"))


def maybe_init_ray() -> bool:
    """Initialise Ray if installed. Returns True when Ray is active.
    Falls back to sequential execution when Ray is absent or fails."""
    try:
        import ray  # type: ignore
    except ImportError:
        logger.info("profile=dev component=continual-trigger ray=absent mode=sequential")
        return False
    try:
        address = os.environ.get("RAY_ADDRESS")  # e.g. "auto" for a cluster
        ray.init(address=address, ignore_reinit_error=True, include_dashboard=False)
        logger.info(
            "profile=prod component=continual-trigger ray=%s mode=%s",
            ray.__version__, "cluster" if address else "local",
        )
        return True
    except Exception as exc:
        logger.warning("component=continual-trigger ray.init failed (%s); sequential", exc)
        return False


def run_continual(since: str, use_ray: bool) -> int:
    """Run the continual-training contract command. Sequential fallback is
    identical minus the Ray runtime initialisation."""
    cmd = [sys.executable, "-m", "training.continual", "--since", since]
    if use_ray:
        cmd += ["--ray"]
    env = dict(os.environ, PYTHONPATH=os.environ.get("PYTHONPATH", ".") )
    logger.info("component=continual-trigger run cmd=%s", " ".join(cmd))
    proc = subprocess.run(cmd, env=env, capture_output=True, text=True)
    if proc.stdout:
        logger.info("continual stdout: %s", proc.stdout[-4000:])
    if proc.returncode != 0:
        logger.error("continual failed rc=%s stderr=%s", proc.returncode, proc.stderr[-4000:])
    return proc.returncode


def main(argv=None) -> int:  # pragma: no cover - scheduler loop
    parser = argparse.ArgumentParser(description="Meridian ML continual-training trigger")
    parser.add_argument("--since", default=DEFAULT_WINDOW, help="training window, e.g. 24h, 7d")
    parser.add_argument("--interval-hours", type=float, default=DEFAULT_INTERVAL_H)
    parser.add_argument("--once", action="store_true", help="run one cycle and exit")
    args = parser.parse_args(argv)

    use_ray = maybe_init_ray()

    while True:
        rc = run_continual(args.since, use_ray)
        if args.once:
            return rc
        logger.info("component=continual-trigger sleeping %.1fh", args.interval_hours)
        time.sleep(args.interval_hours * 3600)


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
