# site-lint mutation corpus

Two miniature sites. `clean/` satisfies every check in `tools/site-lint.sh`;
`broken/` carries exactly one instance of each finding the linter can report.

## Why every file ends in `.in`

These are deliberately-broken fixtures. `broken/index.html.in` has no `<title>`
on purpose — it is how `tools/lib/site-lint_test.sh` proves the missing-title
check fires.

Committed as `.html`, SonarCloud's automatic analysis reads them as production
pages: the missing title reports as a **BUG** that drops the reliability rating,
and the near-identical clean siblings report as **duplication**. Both readings
are exactly backwards, and both failed the quality gate on PR #1943.

A `sonar-project.properties` with `sonar.exclusions` does not help — automatic
analysis ignores the file; the same 36 findings came back with it committed.
HTML has no `NOSONAR` comment in this setup either (that is a shell-only
facility here). The extension is the lever that works.

`site-lint_test.sh` stages both trees into a temp directory, stripping the
suffix, and asserts the file count it staged — so a staging bug fails loudly
rather than handing the linter an empty site.
