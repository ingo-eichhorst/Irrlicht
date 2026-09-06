# What Claude Desktop cannot be driven to do

`recipe-census.txt` plans every recipe against the driver's step grammar and
names the missing **control** for each one it cannot drive. `harness-gaps.md`
covers the cells whose obstacle is the harness rather than a control. This file
covers a third kind, found by driving the app rather than by reading it: things
the Desktop front end itself does not do, which no driver change can supply.

Measured against Claude Desktop 1.46388.4 with bundled Claude Code 2.1.260, on
2026-09-06 and 2026-09-07. Every figure below comes from a tree the driver
saved at the moment of refusal.

## There is no Stop control, at any point in a turn — 2-20

The catalog believed Desktop swapped the composer's Send button for a Stop
button while a turn ran. It does not.

```sh
jq '[.[] | select(.description=="Stop" or .title=="Stop")] | length' \
  replaydata/agents/claudecode/desktop-evidence/no-stop-control-1.46388.4.json
jq '[.[] | select(.description=="Send")] | length' \
  replaydata/agents/claudecode/desktop-evidence/no-stop-control-1.46388.4.json
jq 'length' replaydata/agents/claudecode/desktop-evidence/no-stop-control-1.46388.4.json
```

```
0
1
767
```

That tree was captured ten seconds into a streaming turn, by the driver, at the
moment its `interrupt` step was refused. `2-20_user-esc-interrupt` drives that
step, and Escape's postcondition ("Stop gives way to Send") watches for the same
control, so both are undrivable here.

Two consequences elsewhere, recorded where each belief lives: `turnInFlight`
can never be true on this build, and `Submit`'s "the postcondition was missed,
so the click landed" path is not an edge case — it is the only way a submit
ever completes.

## The agent has no task-list tool — 2-3

Asked to drive one, the Desktop agent made eight ToolSearch calls and then said
so itself:

> TodoWrite, TaskCreate, checklist) and found no match.
> I cannot complete this instruction as written. Tell me how to proceed.

Its turn ended `waiting` on that question, where the spec asserts `ready`. The
driver typed the prompt, the session was created and observed, and the recipe
is the same three steps its six tool-using peers use — 2-2, 2-4, 2-21, 3-1, 3-3
and 2-13. The gap is the toolset, not the rig.

## The agent does not fan out — 3-5

Four attempts. Three recordings carried no `transcript_new` for any child
session at all; the fourth bore one child and then never produced the
parent-child link the spec anchors on. The parent's own turn ended `waiting`
within eleven seconds every time, which is the agent asking a question rather
than launching agents.

```sh
# in a staged run of the cell:
jq -r 'select(.kind=="transcript_new") | .session_id' \
  .build/refresh/claudecode/3-5_workflow-fanout-*/replaydata/agents/claudecode/scenarios/3-5_workflow-fanout/events.jsonl
```

## No blocking dialog without a tool call — 2-27

The cell's second turn prints 300 lines and then answers the expected dialog
with Enter. At that keystroke the tree carried 854 controls and no dialog among
them: no numbered choice buttons, and the composer showing `Send`, so the turn
had already finished.

The driver CAN answer a Desktop dialog — `2-26` records exactly that, clicking
`Allow once 2` and watching it go away — so this is an elicitation gap. Nothing
in the recipe makes Claude Desktop raise a blocking dialog that is not a tool
permission prompt.

## The owned-session menu cannot be clicked — 2-18

This one is a control the app HAS and the driver cannot reach. The turn drives
and every piece of evidence is captured; only the archive fails, so the run
cannot discharge its ownership obligation and can never end cleanly.

```
the owned-session menu   AXPopUpButton  (2161, -373, 26x26)
a window-management      AXButton "Move" (2165, -376, 44x16)
```

"Move" sits over the menu, and its lower edge falls on the menu's centre y of
-360. The helper now tries seven points inside the control's own frame; across
five attempts a run, over four runs, every one hit-tested to something other
than the menu or a descendant of it.

The sidebar carries the same menu per row, away from the window chrome, but it
cannot identify this session: five unarchived sessions share the title, because
Desktop names a session after its content and every repeat of a scenario earns
the same name.
