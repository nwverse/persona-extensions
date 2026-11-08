#!/usr/bin/env python3
"""Check the single signed catalog. Publishing generates an unsigned payload in a separate artifact."""
import argparse
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import catalog

ROOT = str(catalog.ROOT)
NEW = str(catalog.CATALOG)


def check(path=None):
    try:
        catalog.verified(path or NEW)
        return True
    except (ValueError, OSError, TypeError, KeyError) as error:
        print("catalog: " + str(error), file=sys.stderr)
        return False


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["check"])
    parser.add_argument("--catalog", default=NEW)
    args = parser.parse_args()
    sys.exit(0 if check(args.catalog) else 1)
