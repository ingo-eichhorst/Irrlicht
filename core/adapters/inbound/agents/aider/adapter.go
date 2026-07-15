package aider

// AdapterName identifies sessions originating from the Aider coding agent.
const AdapterName = "aider"

// ProcessName is the OS-level executable name for Aider. It is not used for
// process matching: Aider runs under python, so the adapter matches on the
// command line instead (agent.CommandPattern in agent.go), and the scanner's
// command-line path bypasses `pgrep -x` entirely — see processNameFor in
// cmd/irrlichd/wiring.go. Nothing outside this declaration reads it today.
const ProcessName = "aider"
