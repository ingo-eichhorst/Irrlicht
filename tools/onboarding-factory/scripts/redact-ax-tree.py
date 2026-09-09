#!/usr/bin/env python3
"""Redact an accessibility tree before it is committed as evidence.

A tree dumped from a live Claude Desktop window carries the operator's screen,
not just the control under test. Cell 2-20's evidence tree shipped the macOS
Recent Items menu — four document filenames — into a public repository, and ten
conversation titles with it.

The redaction is structural, so it holds for any future capture:

  1. Every element under `AXMenuBar` is dropped. That subtree is the macOS menu
     bar, which carries Recent Items. No driver control lives there — the slash
     popup selector excludes it explicitly — so nothing of evidentiary value is
     lost, and the element count before and after is reported so a claim about
     the tree can cite both.
  2. Conversation titles are pseudonymised. Claude Desktop names a session after
     its content, so the sidebar is a list of what the operator worked on. Equal
     titles stay equal within a file, because several committed findings turn on
     several sessions sharing one name.
  3. Home paths and email addresses are replaced.

Anything else operator-specific — an account display name, say — has no
structural signature, so it is passed with --scrub and is not committed here.

The script is idempotent: a redacted tree passes through unchanged.

    redact-ax-tree.py [--scrub STRING]... FILE...
"""

import argparse
import json
import os
import pathlib
import re
import sys

SESSION_PREFIX = "More options for "
PSEUDONYM = re.compile(r"^session-\d+$")
HOME_PATH = re.compile(r"/Users/[^/\s\"]+")
EMAIL = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+")
TEXT_FIELDS = ("title", "description", "value")
ACCOUNT_PLACEHOLDER = "«account»"
EMAIL_PLACEHOLDER = "«email»"
REDACTED_HOME = "/Users/«operator»"


def tree_path(name):
    """Resolve one command-line argument to a file inside the working tree.

    This script rewrites files in place. A path that escapes the directory
    it was invoked from is refused rather than resolved, so a mistyped or
    machine-generated argument cannot overwrite something outside it.
    """
    root = str(pathlib.Path.cwd().resolve())
    candidate = str(pathlib.Path(name).resolve())
    if not candidate.startswith(root + os.sep):
        raise ValueError("%s lies outside %s" % (candidate, root))
    path = pathlib.Path(candidate)
    if not path.is_file():
        raise ValueError("%s is not a file" % candidate)
    return path


def session_pseudonyms(elements):
    """Map every conversation title to session-N, in first-appearance order.

    Equal titles must stay equal: several committed findings turn on Desktop
    naming two sessions the same, because it names a session after its content.
    An already-pseudonymised title is left alone, which is what makes a second
    run of this script a no-op.
    """
    names = {}
    for element in elements:
        description = element.get("description") or ""
        if not description.startswith(SESSION_PREFIX):
            continue
        title = description[len(SESSION_PREFIX):]
        if title and not PSEUDONYM.match(title) and title not in names:
            names[title] = "session-%d" % (len(names) + 1)
    return names


def rewrite_text(text, names, scrub, counts):
    for real, alias in names.items():
        if real in text:
            text = text.replace(real, alias)
            counts["titles"] += 1
    for needle in scrub:
        if needle and needle in text:
            text = text.replace(needle, ACCOUNT_PLACEHOLDER)
            counts["scrubbed"] += 1
    text, replaced = HOME_PATH.subn(REDACTED_HOME, text)
    counts["paths"] += replaced
    text, replaced = EMAIL.subn(EMAIL_PLACEHOLDER, text)
    counts["emails"] += replaced
    return text


def redact(elements, scrub):
    kept = [e for e in elements if "AXMenuBar" not in (e.get("hierarchy") or [])]
    counts = {"menubar": len(elements) - len(kept),
              "titles": 0, "paths": 0, "emails": 0, "scrubbed": 0}
    names = session_pseudonyms(kept)
    for element in kept:
        for field in TEXT_FIELDS:
            if isinstance(element.get(field), str):
                element[field] = rewrite_text(element[field], names, scrub, counts)
    return kept, counts


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--scrub", action="append", default=[],
                        help="an operator-specific string to replace; repeatable")
    parser.add_argument("files", nargs="+")
    args = parser.parse_args()

    failed = False
    for name in args.files:
        try:
            path = tree_path(name)
            elements = json.loads(path.read_text())
        except (OSError, ValueError) as err:
            # An input this cannot read is the LAST place to fall silent: it is
            # the case where nothing gets redacted at all.
            print("REFUSED %s: %s" % (name, err), file=sys.stderr)
            failed = True
            continue
        if not isinstance(elements, list):
            print("REFUSED %s: an accessibility tree must be a JSON array" % name,
                  file=sys.stderr)
            failed = True
            continue
        before = len(elements)
        kept, counts = redact(elements, args.scrub)
        path.write_text(json.dumps(kept, indent=2, ensure_ascii=False) + "\n")
        print("%s: %d -> %d elements (menu-bar rows dropped %d, "
              "title replacements %d, paths %d, emails %d, scrubbed %d)"
              % (path.name, before, len(kept), counts["menubar"], counts["titles"],
                 counts["paths"], counts["emails"], counts["scrubbed"]))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
