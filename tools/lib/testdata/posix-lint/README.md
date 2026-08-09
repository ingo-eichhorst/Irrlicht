# posix-lint fixture corpus

Deliberately-broken `#!/bin/sh` scripts, one bashism class per file, plus a
clean control. They exist so `tools/posix-lint.sh` can be shown *capable of
failing* — a gate that passes by construction is exactly the defect #1423 was
filed about.

`tools/lib/posix-lint_test.sh` drives this directory. The gate itself excludes
`*/testdata/*` from its own walk, so these files never fail CI — the same
split `tools/skill-lint.sh` draws for its corpus under `testdata/skill-lint/`.

Do not "fix" the bashisms in `bad-*.sh`. They are the assertions.
