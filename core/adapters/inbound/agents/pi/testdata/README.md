# pi adapter testdata

`real-pi-settings.json` is a VERBATIM copy of a real `~/.pi/agent/settings.json`
written by pi 0.83.0 itself, captured on 2026-08-22 (issue #1756: an install
should be graded against a config the agent actually wrote, not one the test
constructed). Nothing was redacted because nothing in it is a secret — pi keeps
credentials in a separate `auth.json`, which is deliberately NOT captured here.

Its `packages` array is the load-bearing part: it is what `pi install` writes,
so this fixture is a home where the OTHER pi extension mechanism is already in
use. `TestInstallLeavesAPiWrittenSettingsFileUntouched` asserts irrlicht's
install does not touch it.
