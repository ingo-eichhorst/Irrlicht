Mutation fixtures for the Desktop driver's scraped grammar declaration.

Each file is a `driver-desktop.sh` whose declaration has been changed in exactly
one way. `contract_test.go`'s `TestDeclarationGuardGoesRedOnEveryCommittedMutation`
runs the real drift guard over every one of them and requires it to go RED.

They are committed rather than described because a guard whose green was never
seen against a mutated input is a claim, not evidence (AGENTS.md, "anything a
change adds has no before to run red — mutate the thing it protects instead").
