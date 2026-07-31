"""Synthetic Nigerian transaction generator for the Meridian tax platform.

Realism requirements (plan-ml.md Stage 1):
- integer kobo amounts (lognormal naira underlying)
- salary-cycle seasonality (25th -> 5th of each month)
- weekly market-day pattern
- channels: pos / agent / ussd / einvoice / pssp
- Nigerian merchant / category mix
- labeled fraud typologies:
    structuring_under_threshold  -- repeated amounts just under the N10m (1e9 kobo) reporting threshold
    agent_collusion_ring         -- graph communities of entities funneling via a shared agent (GNN target)
    account_takeover_burst       -- burst of high-value txns at odd hours on an established account
    vat_invoice_padding          -- einvoice rows with padded amounts / mismatched VAT
    dormant_reactivation         -- long-dormant account suddenly reactivating with large txns
- builds entity-transaction graph (node features + adjacency) for the GNN.
"""
from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np
import pandas as pd

CHANNELS = ["pos", "agent", "ussd", "einvoice", "pssp"]
CATEGORIES = [
    "market_trade", "provisions", "transport", "telecoms", "agro_inputs",
    "building_materials", "fashion", "electronics", "hospitality",
    "professional_services", "pharmacy", "fuel",
]
# Rough share of each channel in legitimate volume
CHANNEL_P = np.array([0.34, 0.22, 0.16, 0.13, 0.15])

THRESHOLD_KOBO = 1_000_000_000  # N10m reporting threshold in kobo
HOUR_P = np.array([
    0.3, 0.15, 0.1, 0.08, 0.08, 0.15,  # 00-05
    0.7, 1.2, 2.0, 2.6, 2.8, 2.7,      # 06-11
    2.6, 2.4, 2.2, 2.0, 1.9, 1.8,      # 12-17
    1.9, 2.1, 1.9, 1.4, 0.9, 0.5,      # 18-23
])
HOUR_P = HOUR_P / HOUR_P.sum()

# Per-transaction feature columns consumed by the ML models (keep in sync with models/)
FEATURES = [
    "log_amount", "hour_sin", "hour_cos", "dow_sin", "dow_cos",
    "is_salary_window", "is_market_day", "is_night",
    "ch_pos", "ch_agent", "ch_ussd", "ch_einvoice", "ch_pssp",
    "tx_count_7d", "tx_sum_7d_log", "days_since_prev_tx",
    "is_new_counterparty", "amount_vs_p90", "vat_rate",
]
N_FEATURES = len(FEATURES)


@dataclass
class SyntheticData:
    transactions: pd.DataFrame      # one row per transaction incl. label + features
    graph: dict                     # {x, edge_index, adj, y, node_kind, ring_nodes}
    ring_members: list[list[int]]   # entity node ids of each collusion ring


def _salary_window(day_of_month: np.ndarray) -> np.ndarray:
    """True for 25th..31st and 1st..5th (salary cycle)."""
    return ((day_of_month >= 25) | (day_of_month <= 5)).astype(float)


def generate(n_entities: int = 1500, n_agents: int = 80, days: int = 120,
             n_rings: int = 5, fraud_rate: float = 0.04, seed: int = 42) -> SyntheticData:
    rng = np.random.default_rng(seed)
    n_accounts = n_entities + n_agents
    entity_ids = np.arange(n_entities)
    agent_ids = np.arange(n_entities, n_accounts)

    # entity meta
    home_region = rng.integers(0, 6, n_entities)          # 6 regions, each with own market day
    region_market_dow = rng.permutation(7)[:6]            # market day of week per region
    preferred_channel = rng.choice(len(CHANNELS), n_entities, p=[0.35, 0.25, 0.15, 0.10, 0.15])
    category = rng.choice(len(CATEGORIES), n_entities)
    # lognormal naira mu per entity -> integer kobo
    mu = rng.normal(9.2, 1.0, n_entities)                 # ~N10k median, wide spread
    dormant = rng.random(n_entities) < 0.08               # some accounts dormant

    # -- collusion rings: groups of entities + one shared agent ----------------
    ring_members: list[list[int]] = []
    ring_agent: list[int] = []
    in_ring = np.zeros(n_entities, dtype=bool)
    for r in range(n_rings):
        size = int(rng.integers(8, 20))
        members = rng.choice(entity_ids[~in_ring], size=min(size, (~in_ring).sum()), replace=False)
        in_ring[members] = True
        ring_members.append(members.tolist())
        ring_agent.append(int(rng.choice(agent_ids)))

    # -- base transaction counts per day with seasonality ----------------------
    dates = pd.date_range("2024-01-01", periods=days, freq="D")
    rows = []

    def volume_mult(d: pd.Timestamp) -> float:
        m = 1.0
        if d.day >= 25 or d.day <= 5:
            m *= 1.9                      # salary cycle
        m *= [0.85, 0.95, 1.0, 1.0, 1.05, 1.25, 0.9][d.dayofweek]  # weekly shape
        return m

    tx_id = 0
    for d in dates:
        lam = n_entities * 0.35 * volume_mult(d)
        n_tx = int(rng.poisson(lam))
        ent = rng.choice(entity_ids, size=n_tx)
        for e in ent:
            if dormant[e] and rng.random() < 0.97:
                continue
            dow = d.dayofweek
            is_market = float(dow == region_market_dow[home_region[e]])
            ch = int(preferred_channel[e]) if rng.random() < 0.7 else int(rng.choice(len(CHANNELS), p=CHANNEL_P))
            if is_market and ch in (0, 1) and rng.random() < 0.4:
                continue  # duplicate market-day tx? keep simple: small skip
            sigma = 0.9
            amount_naira = rng.lognormal(mu[e], sigma)
            amount_kobo = int(max(100, round(amount_naira * 100)))  # integer kobo, min N1
            hour = int(rng.choice(24, p=HOUR_P))
            is_pos_agent = ch in (0, 1)
            counterparty = int(rng.choice(agent_ids)) if is_pos_agent else -1
            vat = 0.075 if ch == 3 else 0.0
            rows.append((tx_id, int(e), counterparty, d, hour, ch, int(category[e]),
                         amount_kobo, vat, 0, ""))
            tx_id += 1

    df = pd.DataFrame(rows, columns=[
        "tx_id", "entity", "counterparty", "date", "hour", "channel", "category",
        "amount_kobo", "vat_rate", "label", "fraud_type"])

    # -- inject fraud typologies ------------------------------------------------
    n_fraud = max(50, int(fraud_rate * len(df)))
    per_type = n_fraud // 5
    frows = []
    fid_start = df["tx_id"].max() + 1

    def add(ent, cp, date, hour, ch, cat, amount, vat, ftype):
        frows.append((fid_start + len(frows), int(ent), int(cp), date, int(hour), int(ch),
                      int(cat), int(amount), float(vat), 1, ftype))

    # 1. structuring under N10m threshold: repeated 8.5m-9.9m naira txns
    for i in range(per_type):
        e = int(rng.choice(entity_ids))
        d = pd.Timestamp(dates[rng.integers(0, days)])
        amt = int(rng.uniform(8_500_000, 9_950_000) * 100)
        add(e, -1, d, int(rng.integers(8, 20)), int(rng.choice([0, 1, 4])), int(rng.integers(0, len(CATEGORIES))), amt, 0.0, "structuring")

    # 2. agent collusion rings: ring members funnel volume through shared agent
    ring_tx_entity = np.zeros(n_accounts, dtype=int)  # markers for graph labels
    for r in range(n_rings):
        agent = ring_agent[r]
        for member in ring_members[r]:
            for _ in range(max(4, per_type // (n_rings * max(1, len(ring_members[r]))) )):
                d = pd.Timestamp(dates[rng.integers(0, days)])
                amt = int(rng.lognormal(13.5, 0.5) * 100)  # large-ish
                add(member, agent, d, int(rng.integers(7, 22)), 1, int(category[member]), amt, 0.0, "collusion_ring")
                ring_tx_entity[member] = 1
                ring_tx_entity[agent] = 1

    # 3. account takeover bursts: 4-8 txns within one night, high value, ussd/pssp
    for _ in range(max(1, per_type // 6)):
        e = int(rng.choice(entity_ids))
        d = pd.Timestamp(dates[rng.integers(0, days)])
        for _ in range(int(rng.integers(4, 9))):
            amt = int(rng.lognormal(14.5, 0.6) * 100)
            add(e, -1, d, int(rng.choice([1, 2, 3, 4])), int(rng.choice([2, 4])), int(category[e]), amt, 0.0, "account_takeover")

    # 4. VAT invoice padding: einvoice, inflated amount, wrong vat (e.g. 0.5%-2%)
    for i in range(per_type):
        e = int(rng.choice(entity_ids))
        d = pd.Timestamp(dates[rng.integers(0, days)])
        amt = int(rng.lognormal(15.0, 0.7) * 100)
        add(e, -1, d, int(rng.integers(8, 18)), 3, int(category[e]), amt, float(rng.uniform(0.005, 0.02)), "vat_padding")

    # 5. dormant reactivation: dormant account, huge single txn after gap
    dormant_ids = entity_ids[dormant]
    for i in range(min(per_type, max(1, len(dormant_ids)))):
        e = int(dormant_ids[i % len(dormant_ids)])
        d = pd.Timestamp(dates[rng.integers(days // 2, days)])
        amt = int(rng.lognormal(15.2, 0.5) * 100)
        add(e, -1, d, int(rng.integers(0, 24)), int(rng.choice([0, 2, 4])), int(category[e]), amt, 0.0, "dormant_reactivation")

    fdf = pd.DataFrame(frows, columns=df.columns)
    df = pd.concat([df, fdf], ignore_index=True).sort_values(["date", "tx_id"]).reset_index(drop=True)

    df = add_features(df)
    graph = build_graph(df, n_accounts, ring_tx_entity)
    return SyntheticData(transactions=df, graph=graph, ring_members=ring_members)


def add_features(df: pd.DataFrame) -> pd.DataFrame:
    """Compute the FEATURES columns. Works on any dataframe with the raw columns."""
    df = df.copy()
    amt = df["amount_kobo"].astype(float) / 100.0  # naira
    df["log_amount"] = np.log1p(amt)
    df["hour_sin"] = np.sin(2 * math.pi * df["hour"] / 24)
    df["hour_cos"] = np.cos(2 * math.pi * df["hour"] / 24)
    dow = pd.to_datetime(df["date"]).dt.dayofweek
    df["dow_sin"] = np.sin(2 * math.pi * dow / 7)
    df["dow_cos"] = np.cos(2 * math.pi * dow / 7)
    dom = pd.to_datetime(df["date"]).dt.day
    df["is_salary_window"] = _salary_window(dom.to_numpy())
    df["is_market_day"] = (dow == 5).astype(float)  # Saturday market proxy for frame-level use
    df["is_night"] = ((df["hour"] <= 5)).astype(float)
    for i, name in enumerate(CHANNELS):
        df[f"ch_{name}"] = (df["channel"] == i).astype(float)

    df = df.sort_values(["entity", "date", "tx_id"]).reset_index(drop=True)
    # rolling 7d per-entity counts/sums (approximate with groupby rolling on daily aggregates)
    df["date_only"] = pd.to_datetime(df["date"]).dt.floor("D")
    daily = df.groupby(["entity", "date_only"]).agg(cnt=("tx_id", "size"), s=("amount_kobo", "sum")).reset_index()
    daily = daily.sort_values(["entity", "date_only"])
    daily["tx_count_7d"] = daily.groupby("entity")["cnt"].transform(lambda s: s.rolling(7, min_periods=1).sum())
    daily["tx_sum_7d"] = daily.groupby("entity")["s"].transform(lambda s: s.rolling(7, min_periods=1).sum())
    df = df.merge(daily[["entity", "date_only", "tx_count_7d", "tx_sum_7d"]], on=["entity", "date_only"], how="left")
    df["tx_sum_7d_log"] = np.log1p(df["tx_sum_7d"].astype(float) / 100.0)
    df["tx_count_7d"] = np.log1p(df["tx_count_7d"].astype(float))

    prev_date = df.groupby("entity")["date_only"].shift(1)
    df["days_since_prev_tx"] = (df["date_only"] - prev_date).dt.days.fillna(60).clip(0, 90) / 90.0
    prev_cp = df.groupby("entity")["counterparty"].shift(1)
    df["is_new_counterparty"] = ((df["counterparty"] != prev_cp) & (df["counterparty"] >= 0)).astype(float)
    p90 = df.groupby("entity")["amount_kobo"].transform(lambda s: s.quantile(0.9))
    df["amount_vs_p90"] = (df["amount_kobo"] / p90.clip(lower=1)).clip(0, 10) / 10.0
    df["vat_rate"] = df["vat_rate"].fillna(0.0).clip(0, 0.2) / 0.2
    df = df.drop(columns=["date_only", "tx_sum_7d"]).sort_values("tx_id").reset_index(drop=True)
    return df


def build_graph(df: pd.DataFrame, n_accounts: int, ring_labels: np.ndarray | None = None) -> dict:
    """Entity-agent bipartite -> homogeneous projection for the GCN.

    Nodes = accounts (entities + agents). Edge (u,v) if u and v interacted
    (entity->agent), plus entity--entity edges when they share an agent
    (projection), which is what lets ring communities show up.
    """
    sub = df[df["counterparty"] >= 0]
    # weighted edges: weight = number of interactions
    from collections import defaultdict
    ew: dict[tuple[int, int], float] = defaultdict(float)
    by_agent: dict[int, list[int]] = defaultdict(list)
    for e, a in zip(sub["entity"].to_numpy(), sub["counterparty"].to_numpy()):
        e, a = int(e), int(a)
        ew[(e, a)] += 1.0
        ew[(a, e)] += 1.0
        by_agent[a].append(e)
    # projection: entities sharing an agent, edge weight = min(co-occurrence counts)
    cnt: dict[tuple[int, int], int] = defaultdict(int)
    for e, a in zip(sub["entity"].to_numpy(), sub["counterparty"].to_numpy()):
        cnt[(int(e), int(a))] += 1
    for a, members in by_agent.items():
        members = sorted(set(members))
        strong = [m for m in members if cnt[(m, a)] >= 2]
        for i in range(len(strong)):
            for j in range(i + 1, min(len(strong), i + 6)):
                w = min(cnt[(strong[i], a)], cnt[(strong[j], a)])
                ew[(strong[i], strong[j])] += 0.5 * w
                ew[(strong[j], strong[i])] += 0.5 * w
    edges = sorted(ew)
    ei = np.array(edges, dtype=np.int64).T if edges else np.zeros((2, 0), dtype=np.int64)
    weights = np.array([ew[tuple(e)] for e in ei.T], dtype=np.float32) if edges else np.zeros(0, dtype=np.float32)
    weights = weights / max(weights.max(), 1.0)  # scale to (0,1]

    # node features: per-account aggregates
    g = df.groupby("entity")
    x = np.zeros((n_accounts, 8), dtype=np.float32)
    agg = g.agg(n=("tx_id", "size"), total=("amount_kobo", "sum"), mean=("amount_kobo", "mean"),
                maxa=("amount_kobo", "max"), night=("is_night", "mean"), fraud=("label", "max"))
    for idx, row in agg.iterrows():
        x[int(idx)] = [math.log1p(row["n"]), math.log1p(row["total"] / 100), math.log1p(row["mean"] / 100),
                       math.log1p(row["maxa"] / 100), row["night"], 0.0, 1.0, 0.0]
    # agents: aggregate as counterparties
    sub2 = df[df["counterparty"] >= 0].groupby("counterparty").agg(n=("tx_id", "size"), total=("amount_kobo", "sum"))
    for idx, row in sub2.iterrows():
        x[int(idx)] = [math.log1p(row["n"]), math.log1p(row["total"] / 100), 0.0, 0.0, 0.0, 0.0, 0.0, 1.0]
    # normalize features columnwise
    mu, sd = x.mean(0), x.std(0) + 1e-6
    x = (x - mu) / sd

    y = np.zeros(n_accounts, dtype=np.int64)
    if ring_labels is not None:
        y = ring_labels.astype(np.int64)
    else:
        for idx, row in agg.iterrows():
            y[int(idx)] = int(row["fraud"])

    # weighted adjacency with self loops, symmetric normalized D^-1/2 (A+I) D^-1/2
    n = n_accounts
    A = np.zeros((n, n), dtype=np.float32)
    if ei.shape[1]:
        A[ei[0], ei[1]] = weights
    A = np.maximum(A, A.T)
    A += np.eye(n, dtype=np.float32)
    deg = A.sum(1)
    dinv = 1.0 / np.sqrt(np.maximum(deg, 1e-6))
    adj = (dinv[:, None] * A) * dinv[None, :]
    return {"x": x.astype(np.float32), "edge_index": ei, "adj": adj.astype(np.float32),
            "y": y, "n_nodes": n}


if __name__ == "__main__":
    d = generate(n_entities=300, n_agents=20, days=30, n_rings=2, seed=1)
    print(d.transactions["fraud_type"].value_counts())
    print("tx:", len(d.transactions), "graph nodes:", d.graph["n_nodes"], "ring positives:", int(d.graph["y"].sum()))
