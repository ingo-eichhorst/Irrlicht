# Claude Desktop Accessibility helper

`claude-desktop-helper` is a macOS command-line helper for the onboarding
factory. It reads and controls the installed Claude Desktop application through
the macOS Accessibility and Core Graphics APIs.

The helper does not use AppleScript. It does not invoke the Claude Code binary
that Claude Desktop installs. It does not accept screen coordinates.
The shared test script enforces these safety rules before it builds the package.

## Build and test

Run:

```bash
tools/onboarding-factory/desktop-helper/test.sh
```

Swift Package Manager writes all generated files and binaries to the
repository-root `.build/claude-desktop-helper/` directory.

## JSON protocol

Write one JSON object to standard input. Read one JSON object from standard
output. The current `protocolVersion` is `1`. An error returns `ok: false`, a
stable error code, a short message, and a non-zero process status.

This request checks installation, process state, Accessibility access, and
bundle versions:

```json
{"protocolVersion":1,"command":"preflight"}
```

This request returns a bounded Accessibility snapshot:

```json
{
  "protocolVersion": 1,
  "command": "inspect",
  "limits": {"maxDepth": 64, "maxNodes": 5000}
}
```

The snapshot includes role, subrole, title, description, identifier, hierarchy,
state, path, and current frame. The snapshot does not include text-field values.
The helper accepts a maximum depth of 128 and a maximum node count of 50,000.

A `probe` request supplies named controls and one or more selectors for each
control. A later driver can keep its version-specific control catalog separate
from this low-level helper. Use stable identifiers, roles, and hierarchy when
the application exposes them. Do not use localized text as a primary selector.

```json
{
  "protocolVersion": 1,
  "command": "probe",
  "probes": [
    {
      "name": "prompt_field",
      "selectors": [{"role": "AXTextArea", "identifier": "prompt-input"}],
      "required": true,
      "requiresGeometry": true
    }
  ]
}
```

`set_value` verifies the resulting value. `keyboard` and `physical_click`
require an explicit postcondition. The helper polls that postcondition to a
bounded deadline. The helper derives each click point from a new read of the
selected control frame. An action is refused when its postcondition is already
true before the action. This false-to-true rule prevents an ineffective action
from reporting success.

The supported postconditions are `exists`, `absent`, `enabled`, `disabled`,
`focused`, and `value_equals`. The helper never includes request values in a
response or an error.

## Exit codes

| Exit | Error code |
|---:|---|
| 2 | `invalid_request` |
| 3 | `unsupported_protocol` |
| 10 | `app_not_installed` |
| 11 | `app_not_running` |
| 12 | `accessibility_denied` |
| 13 | `version_metadata_missing` |
| 20 | `traversal_limit` |
| 21 | `control_missing` |
| 22 | `control_ambiguous` |
| 23 | `stale_control` |
| 24 | `action_failed` |
| 25 | `postcondition_failed` |
