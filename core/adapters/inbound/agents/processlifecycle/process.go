package processlifecycle

import "irrlicht/core/ports/outbound"

// osProc is the process-observation mechanism for the platform this binary
// was built for. newObserver is defined once per OS (process_darwin.go,
// process_linux.go, process_other.go) and selected at compile time by build
// tag. Every OS primitive used by the discovery helpers (DiscoverPIDByCWD,
// LiveCWDs, DiscoverPIDByTranscriptWriter) and the Scanner routes through
// this single seam, so adding a platform never touches orchestration.
var osProc outbound.ProcessObserver = newObserver()
