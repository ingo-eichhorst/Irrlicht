#!/usr/bin/env python3
"""Regenerate testdata/Cookies-v24 and testdata/Cookies-v23 (issue #2012).

These are Chrome-format cookie databases encrypted with a KNOWN TEST
password, so chromecookies' decryption is checked against bytes that were
not produced by the Go code under test: the key is derived with Python's
hashlib and every value is encrypted by the openssl CLI.

Run from the repository root, isolated:

    python3 -I core/adapters/outbound/chromecookies/testdata/make_fixture.py

Format, from Chromium source at 6d34471112e9339f8b2b6f8a9566e46d2142b75c:
PBKDF2-HMAC-SHA1(password, b"saltysalt", 1003, 16 bytes), AES-128-CBC with an
IV of sixteen spaces and PKCS#7 padding, the 3-byte prefix b"v10", and from
meta version 24 the plaintext is SHA-256(host_key) || value.

No real cookie is in here. Every value is invented.
"""

import hashlib
import os
import sqlite3
import subprocess
import sys

PASSWORD = "irrlicht-test-safe-storage-password"
HERE = os.path.dirname(os.path.abspath(__file__))

# Microseconds since 1601-01-01 UTC (Chrome's expires_utc epoch).
WINDOWS_EPOCH_OFFSET_US = 11644473600 * 1_000_000
FAR_FUTURE = WINDOWS_EPOCH_OFFSET_US + 4102444800 * 1_000_000  # 2100-01-01
LONG_AGO = WINDOWS_EPOCH_OFFSET_US + 978307200 * 1_000_000  # 2001-01-01

# (host_key, name, value, expires_utc, has_expires, path, creation_utc)
ROWS = [
    ("console.mistral.ai", "csrftoken", "csrf-fixture-value", 0, 0, "/", 13300000000000001),
    (".mistral.ai", "ory_session_coolcurranf83m3srkfl", "ory-fixture-session", FAR_FUTURE, 1, "/", 13300000000000002),
    # Same host, not selected by name.
    (".mistral.ai", "ajs_anonymous_id", "must-not-leave-1", FAR_FUTURE, 1, "/", 13300000000000003),
    # Selected name prefix, but a host that is not selected.
    ("admin.mistral.ai", "ory_session_admin", "must-not-leave-2", FAR_FUTURE, 1, "/", 13300000000000004),
    # Another site entirely.
    (".google.com", "SID", "must-not-leave-3", FAR_FUTURE, 1, "/", 13300000000000005),
    # Selected by name and host, but expired.
    (".mistral.ai", "ory_session_expired", "must-not-leave-4", LONG_AGO, 1, "/", 13300000000000006),
    # Case differs from the prefix: the match is case-sensitive.
    ("console.mistral.ai", "ORY_SESSION_upper", "must-not-leave-5", FAR_FUTURE, 1, "/", 13300000000000007),
    # Would match if "_" in the prefix were a wildcard (SQL LIKE).
    (".mistral.ai", "oryXsessionXwildcard", "must-not-leave-6", FAR_FUTURE, 1, "/", 13300000000000008),
]

SCHEMA = """
CREATE TABLE meta(key LONGVARCHAR NOT NULL UNIQUE PRIMARY KEY, value LONGVARCHAR);
CREATE TABLE cookies(creation_utc INTEGER NOT NULL,host_key TEXT NOT NULL,top_frame_site_key TEXT NOT NULL,
name TEXT NOT NULL,value TEXT NOT NULL,encrypted_value BLOB NOT NULL,path TEXT NOT NULL,expires_utc INTEGER NOT NULL,
is_secure INTEGER NOT NULL,is_httponly INTEGER NOT NULL,last_access_utc INTEGER NOT NULL,has_expires INTEGER NOT NULL,
is_persistent INTEGER NOT NULL,priority INTEGER NOT NULL,samesite INTEGER NOT NULL,source_scheme INTEGER NOT NULL,
source_port INTEGER NOT NULL,last_update_utc INTEGER NOT NULL,source_type INTEGER NOT NULL,
has_cross_site_ancestor INTEGER NOT NULL);
CREATE UNIQUE INDEX cookies_unique_index ON cookies(host_key, top_frame_site_key, has_cross_site_ancestor,
name, path, source_scheme, source_port);
"""


def key() -> bytes:
    return hashlib.pbkdf2_hmac("sha1", PASSWORD.encode(), b"saltysalt", 1003, 16)


def encrypt(plaintext: bytes) -> bytes:
    out = subprocess.run(
        ["openssl", "enc", "-aes-128-cbc", "-K", key().hex(), "-iv", "20" * 16, "-nosalt"],
        input=plaintext, capture_output=True, check=True,
    ).stdout
    return b"v10" + out


def build(path: str, version: int) -> None:
    if os.path.exists(path):
        os.remove(path)
    db = sqlite3.connect(path)
    db.executescript(SCHEMA)
    db.execute("INSERT INTO meta VALUES ('version', ?)", (str(version),))
    db.execute("INSERT INTO meta VALUES ('last_compatible_version', ?)", (str(version),))
    for host, name, value, expires, has_expires, cpath, created in ROWS:
        plaintext = value.encode()
        if version >= 24:
            plaintext = hashlib.sha256(host.encode()).digest() + plaintext
        db.execute(
            "INSERT INTO cookies VALUES (?,?,'',?,'',?,?,?,1,1,?,?,1,1,0,2,443,?,0,0)",
            (created, host, name, encrypt(plaintext), cpath, expires, created, has_expires, created),
        )
    db.commit()
    db.execute("VACUUM")
    db.close()


def main() -> int:
    build(os.path.join(HERE, "Cookies-v24"), 24)
    build(os.path.join(HERE, "Cookies-v23"), 23)
    print("wrote Cookies-v24 and Cookies-v23 under", HERE)
    return 0


if __name__ == "__main__":
    sys.exit(main())
