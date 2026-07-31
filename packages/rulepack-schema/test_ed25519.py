"""RFC 8032 §7.1 test vectors for the pure-Python Ed25519 implementation."""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from ed25519_verify import sign, verify  # noqa: E402

# RFC 8032 TEST 1 (empty message)
SEED1 = bytes.fromhex("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
PUB1 = bytes.fromhex("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a")
SIG1 = bytes.fromhex(
    "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e06522490155"
    "5fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b"
)
# RFC 8032 TEST 2 (1-octet message 0x72)
SEED2 = bytes.fromhex("4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb")
PUB2 = bytes.fromhex("3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c")
SIG2 = bytes.fromhex(
    "92a009a9f0d4cab8720e820b5f642540a2b27b5416503f8fb3762223ebdb69da"
    "085ac1e43e15996e458f3613d0f11d8c387b2eaeb4302aeeb00d291612bb0c00"
)


def test_rfc8032_vector1_empty_message():
    assert sign(SEED1, b"") == SIG1
    assert verify(SIG1, b"", PUB1)


def test_rfc8032_vector2_single_octet():
    assert sign(SEED2, b"\x72") == SIG2
    assert verify(SIG2, b"\x72", PUB2)


def test_verify_rejects_tampering():
    assert not verify(SIG1, b"tampered", PUB1)
    assert not verify(SIG1[:-1] + bytes([SIG1[-1] ^ 1]), b"", PUB1)
    bad_pub = bytes([PUB1[0] ^ 1]) + PUB1[1:]
    assert not verify(SIG1, b"", bad_pub)
