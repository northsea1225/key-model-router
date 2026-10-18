#!/usr/bin/env python3
"""Read CPAMP api_key_aliases (read-only) into a fingerprint -> alias JSON seed.

The plugin never writes to the CPAMP database. Run this as root or as the CPA
user; pass the output path explicitly.
"""

import argparse
import json
import sqlite3
import sys


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    uri = f"file:{args.db}?mode=ro"
    con = sqlite3.connect(uri, uri=True)
    try:
        rows = list(con.execute("select api_key_hash, alias from api_key_aliases"))
    finally:
        con.close()

    out = {}
    for key_hash, alias in rows:
        key_hash = (key_hash or "").strip().lower()
        alias = (alias or "").strip()
        if len(key_hash) == 64 and alias:
            out[key_hash] = alias

    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump(out, fh, ensure_ascii=False, indent=2, sort_keys=True)
        fh.write("\n")
    print(f"wrote {len(out)} aliases to {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
