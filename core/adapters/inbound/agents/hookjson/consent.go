// consent.go holds the one receiver-side type the JSON-hook adapters share
// verbatim. Their HookTarget interfaces and payload structs deliberately stay
// per-adapter — those differ in shape and in intent (Codex, for instance, does
// not decode session_id at all, because a Codex session shares its id with
// every child) and merging them would couple the adapters to each other's
// event sets rather than to a common contract.
package hookjson

// ConsentGranter reports whether the user has granted a permission (issue
// #570). Satisfied by *services.PermissionService. Hooks installed by a
// pre-consent daemon keep firing until the prompt is answered, so a hook
// receiver drops payloads while its permission is pending or denied.
type ConsentGranter interface {
	Granted(agentName, key string) bool
}
