package claudecode

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// AccountName is the pseudo-adapter this permission is declared on. It is
// deliberately NOT a second permission on "claude-code": a KindObserve effect
// calls start/stopWatching for its agent name, so a second observe permission
// there would stop every Claude Code transcript watcher on revoke
// (declaration.go's Permission doc), and would never be consulted anyway.
const AccountName = "claude-code-account"

// PermissionKeyAccount gates the identity-file read.
const PermissionKeyAccount = "account"

// AccountPermissionDeclaration returns the consent declaration for reading the
// Claude Code account id. Like muse-account-api it is a daemon-wide entry with
// no Source/Process axes; unlike it there is nothing to start or stop, so
// Apply and Remove are nil — AccountReader checks the grant on every read.
func AccountPermissionDeclaration() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: AccountName, DisplayName: "Claude Code account"},
		Permissions: []agent.Permission{{
			Key:             PermissionKeyAccount,
			Kind:            permission.KindObserve,
			Title:           "Identify the Claude Code account",
			FeatureUnlocked: "One quota chip per Claude Code account, instead of sessions on different accounts sharing one chip",
			Touches:         "Reads the account id and plan type from Claude Code's identity file (~/.claude.json, or $CLAUDE_CONFIG_DIR/.claude.json when that is set)",
			Detail: "irrlicht reads only two fields from that file: oauthAccount.accountUuid " +
				"and oauthAccount.organizationType. It never reads or keeps your email " +
				"address, name, organisation name, or any token or credential in the " +
				"file — the file is decoded into a structure that has room for those two " +
				"fields and nothing else. It also reads the first timestamp of a " +
				"session's transcript to tell whether the session began before or after " +
				"the last account change. A session is attributed to an account only if " +
				"that account was already logged in when the session began and has not " +
				"changed since; otherwise its quota stays unattributed. Turning this off " +
				"stops these reads immediately. It does not touch transcript monitoring.",
		}},
	}
}
