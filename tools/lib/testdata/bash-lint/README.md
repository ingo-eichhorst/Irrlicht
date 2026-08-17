# bash-lint fixture corpus

Deliberately-broken `bash` scripts, one shellcheck rule class per file, plus two
clean controls. They exist so `tools/bash-lint.sh` can be shown *capable of
failing* — a gate that passes by construction is exactly the defect #1423 and
#1684 were filed about, and evidence living only in a merged PR body is re-run
by nothing.

`tools/lib/bash-lint_test.sh` drives this directory. The gate itself excludes
`*/testdata/*` from its own walk, so these files never fail CI — the same split
`tools/posix-lint.sh` and `tools/skill-lint.sh` draw for their corpora.

## What each file pins

| file | code | why it is here |
|---|---|---|
| `bad-rm-rf-var.sh` | SC2115 | `rm -rf "$var/lib"` → `/lib`. The shape #1684 was filed about. |
| `bad-assign-command.sh` | SC2209 | `var=command` assigns a string and runs nothing. |
| `bad-unused-var.sh` | SC2034 | assigned, never used — a rename that landed in one place. |
| `bad-cd-unchecked.sh` | SC2164 | `cd` with no `\|\| exit`; everything after runs in the wrong tree. |
| `bad-array-brace.sh` | SC1087 | `$var[` read as an array expansion. |
| `bad-declare-assign.sh` | SC2155 | `local x=$(cmd)` masks `cmd`'s status. |
| `bad-redirect-no-command.sh` | SC2188 | a redirection with no command. |
| `bad-directive-prose.sh` | SC1072/SC1073 | a prose comment opening with the linter's name is parsed as a directive, and an unparseable one makes it **abandon the file**. |
| `bad-directive-keyvalue.sh` | SC1125 | a directive whose reason is appended without a second `#`. |
| `good-clean.sh` | — | the **vacuity guard**. A linter that failed everything would satisfy all nine above. |
| `style-noisy-but-warning-clean.sh` | — | the severity floor, in the one direction the `bad-*` files cannot reach: shellcheck exits 1 carrying only SC2086/SC2012, below the floor, and the gate must pass it. |

`bad-directive-prose.sh` is the one to read first, because its failure mode is
silence rather than noise: the SC2115 on its last line is **invisible** while
line 12 is present, and `bash-lint_test.sh` proves that by rewording that one
line and asserting the finding appears. `replaydata/_lib/drive/contracts.sh` has
been carrying the same construct unnoticed (#1687), and `tools/bash-lint.sh`'s own
header tripped it on the gate's first run.

Do not "fix" the findings in `bad-*.sh`. They are the assertions.
