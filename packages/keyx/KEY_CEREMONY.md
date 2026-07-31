# Key Ceremony — HSM/KMS-backed authoritative signing keys

Scope: e-invoice CSID keys, tax-clearance / attribution-feed signing keys,
WORM evidence-receipt signing keys, and QR HMAC secrets, when run under
`KEY_PROVIDER=hsm` (PKCS#11 exec plugin) or `KEY_PROVIDER=cloud-kms`.

## 1. Generation

- Keys are generated **inside** the HSM/KMS (non-exportable). Software
  export of private key material is prohibited for `prod` profiles.
- Algorithm policy: ed25519 for asymmetric signing (CSID, feeds, receipts);
  HMAC-SHA256 (32-byte secret) for QR integrity codes (keyID suffix
  `-hmac`).
- Logical keyIDs (`csid`, `feed`, `receipt`, `qr-hmac`) are mapped to HSM
  slot/token labels or KMS key ARNs/aliases by the operator runbook; the
  mapping is recorded in the ceremony minutes.
- Ceremony requires two officers (see §4) and produces:
  1. key attestation bundle (§2),
  2. the public key fingerprint (`sha256(pubkey)[:8]`, hex) recorded in the
     key registry,
  3. initial key version number (KMS) or token object handle (HSM).

## 2. Attestation

- HSM: export the vendor key-attestation certificate chain (key was
  generated on-device, non-exportable) and verify it against the vendor root
  before first use. For the CGO-free exec plugin (`KEY_PKCS11_PLUGIN`), the
  plugin MUST present the attestation on the `pubkey` op at ceremony time.
- Cloud KMS: record the KMS key metadata (creation time, protection level
  `HSM` where available, key version) plus the cloud audit-log entry for the
  `CreateKey` call.
- The attestation bundle is stored in the WORM evidence store under
  `kind=key-ceremony`.

## 3. Rotation schedule

| Key            | Cadence     | Notes                                        |
| -------------- | ----------- | -------------------------------------------- |
| `csid`         | 12 months   | re-register public key with MBS after rotate |
| `feed` (tcc)   | 12 months   | gateway F7 serves current + previous pubkey  |
| `receipt`      | 12 months   | old receipts remain verifiable (key history) |
| `qr-hmac`      | 6 months    | overlap window: old codes verifiable 30 days |
| unscheduled    | on incident | suspected compromise → immediate rotate      |

`SignerProvider.Rotate(keyID)` creates the new version and makes it active
for signing; verification keys are versioned so historical signatures stay
verifiable. Env-seeded dev keys are rotated by updating the environment
(in-process rotation is refused by design).

## 4. Dual control

- Generation, rotation, and destruction each require **two authorized
  officers** (M-of-N quorum on the HSM, or dual approval workflow on the
  KMS IAM policy). No single operator can sign ceremony keys alone.
- The exec-plugin binary path and its checksum are pinned in deployment
  config; changes to it follow the same dual-approval workflow.

## 5. Fail-closed operation

When `KEY_PROVIDER` names a non-software provider and that provider cannot
be reached/started, services MUST refuse startup (no silent software
fallback). This is enforced by `provider.NewFromEnv` returning
`ErrUnavailable` and callers `log.Fatal`-ing on it. Dev default remains
`software` with file/env keys.
