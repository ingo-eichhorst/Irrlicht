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

`TestCommittedEvidenceTreesCarryNoOperatorContent` is the audit that fails when
a tree reaches the repository without this having been run over it.
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
TEXT_FIELDS = ("title", "description", "value")
ACCOUNT_PLACEHOLDER = "«account»"
EMAIL_PLACEHOLDER = "«email»"
REDACTED_HOME = "/Users/«operator»"


def mask_emails(text):
    """Replace every token shaped like an email address.

    This is deliberately not a regular expression. The natural one repeats a
    host label and then asks for a dotted tail, and the two can match the same
    characters, so a long run without a dot is re-split at every start position.
    Inspecting whitespace-separated tokens is linear and says the same thing.

    The Go audit keeps a regular expression for this, because RE2 does not
    backtrack and cannot have that behaviour.
    """
    if "@" not in text:
        return text, 0
    replaced = 0
    for token in sorted(set(text.split()), key=len, reverse=True):
        local, at, host = token.partition("@")
        if not at or not local:
            continue
        labels = host.split(".")
        if len(labels) < 2 or not all(labels):
            continue
        text = text.replace(token, EMAIL_PLACEHOLDER)
        replaced += 1
    return text, replaced


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
    text, replaced = mask_emails(text)
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

    # The containment check and the write it protects stay in one function on
    # purpose. Expressed through a helper, the taint analysis stopped seeing
    # that the path reaching the write is the path that was validated.
    root = str(pathlib.Path.cwd().resolve())
    failed = False
    for name in args.files:
        target = str(pathlib.Path(name).resolve())
        if not target.startswith(root + os.sep) or not os.path.isfile(target):
            print("REFUSED %s: not a file inside %s" % (name, root), file=sys.stderr)
            failed = True
            continue
        try:
            with open(target, "r", encoding="utf-8") as handle:
                elements = json.load(handle)
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
        with open(target, "w", encoding="utf-8") as handle:
            handle.write(json.dumps(kept, indent=2, ensure_ascii=False) + "\n")
        print("%s: %d -> %d elements (menu-bar rows dropped %d, "
              "title replacements %d, paths %d, emails %d, scrubbed %d)"
              % (os.path.basename(target), before, len(kept), counts["menubar"],
                 counts["titles"], counts["paths"], counts["emails"], counts["scrubbed"]))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
