"""Hand-rolled Metropolis-Hastings Bayesian estimation (numpy only, no numpyro).

Two jobs:
1. Posterior over the platform fraud rate given observed labels
   (Beta-Binomial, but sampled via MH to keep one machinery + allow
   non-conjugate extensions).
2. Posterior over the anomaly-alert threshold on the autoencoder's
   reconstruction-error score, targeting a desired precision, with a
   lognormal prior on the threshold.
"""
from __future__ import annotations

from dataclasses import dataclass

import numpy as np


@dataclass
class PosteriorSummary:
    param: str
    mean: float
    median: float
    ci90: tuple[float, float]
    n_samples: int
    acceptance_rate: float


def _summarize(param: str, samples: np.ndarray, acc: float) -> PosteriorSummary:
    return PosteriorSummary(param, float(samples.mean()), float(np.median(samples)),
                            (float(np.quantile(samples, 0.05)), float(np.quantile(samples, 0.95))),
                            len(samples), float(acc))


def mh_fraud_rate(n_fraud: int, n_total: int, n_samples: int = 8000, burn: int = 1000,
                  proposal_sd: float = 0.05, seed: int = 0) -> PosteriorSummary:
    """Posterior over fraud rate p with logit-normal random-walk MH, Beta(1,1) prior."""
    rng = np.random.default_rng(seed)
    logit = np.log(max(n_fraud, 0.5) / max(n_total - n_fraud, 0.5))
    chain, accepted = [], 0

    def logpost(l: float) -> float:
        p = 1 / (1 + np.exp(-l))
        return n_fraud * np.log(max(p, 1e-12)) + (n_total - n_fraud) * np.log(max(1 - p, 1e-12))

    cur, cur_lp = logit, logpost(logit)
    for i in range(n_samples + burn):
        prop = cur + rng.normal(0, proposal_sd)
        lp = logpost(prop)
        if np.log(rng.random()) < lp - cur_lp:
            cur, cur_lp, accepted = prop, lp, accepted + 1
        if i >= burn:
            chain.append(1 / (1 + np.exp(-cur)))
    return _summarize("fraud_rate", np.array(chain), accepted / (n_samples + burn))


def mh_anomaly_threshold(scores: np.ndarray, labels: np.ndarray, target_precision: float = 0.8,
                         n_samples: int = 6000, burn: int = 1000, proposal_sd: float = 0.3,
                         seed: int = 0) -> PosteriorSummary:
    """Posterior over alert threshold t on anomaly scores.

    Likelihood rewards precision close to target while penalizing tiny alert
    volumes; prior lognormal around the 90th percentile of scores.
    """
    rng = np.random.default_rng(seed)
    scores = np.asarray(scores, dtype=float)
    labels = np.asarray(labels, dtype=int)
    mu0 = np.log(np.quantile(scores, 0.9) + 1e-9)

    def logpost(logt: float) -> float:
        t = np.exp(logt)
        alerts = scores >= t
        n_alert = alerts.sum()
        if n_alert == 0:
            return -1e9
        tp = (alerts & (labels == 1)).sum()
        precision = tp / n_alert
        recall = tp / max((labels == 1).sum(), 1)
        ll = -((precision - target_precision) ** 2) / 0.02 + np.log(max(recall, 1e-9))
        prior = -((logt - mu0) ** 2) / 8.0
        return ll + prior

    cur = mu0
    cur_lp = logpost(cur)
    chain, accepted = [], 0
    for i in range(n_samples + burn):
        prop = cur + rng.normal(0, proposal_sd)
        lp = logpost(prop)
        if np.log(rng.random()) < lp - cur_lp:
            cur, cur_lp, accepted = prop, lp, accepted + 1
        if i >= burn:
            chain.append(np.exp(cur))
    return _summarize("anomaly_threshold", np.array(chain), accepted / (n_samples + burn))
