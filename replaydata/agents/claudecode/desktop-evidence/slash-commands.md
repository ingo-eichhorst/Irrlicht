# Claude Desktop executes slash commands — measured 2026-09-07

`recipe.go` recorded the opposite: *"nothing measured shows the Desktop composer
EXECUTING a slash command rather than storing it as prompt text."* That claim
was true of the dump it was written against. It is **false** about the
application, and it is the cited cause behind six `not-runnable` verdicts.

This note is the measurement that disproves it. Two accessibility trees sit next
to it, both from Claude Desktop 1.46388.4 with bundled Claude Code 2.1.260, and
both with the macOS menu-bar subtree removed (it carries the operator's Recent
Items, and nothing about the composer).

## What was measured

Every step below ran through `claude-desktop-helper` against the live app. Each
keystroke carried a `value_equals` postcondition on the prompt field, so a key
that did not land failed loudly instead of passing silently.

| Step | Command | Result |
| --- | --- | --- |
| 1 | `keyboard` Shift+7 → `/` | 24 `AXMenuItem` elements appear inside `AXWebArea`, at hierarchy depth 15 |
| 2 | `keyboard` `c`,`o`,`n`,`t`,`e`,`x`,`t` | the list filters live; `context` moves to first of 11 |
| 3 | `keyboard` Return | the popup closes and the composer holds the accepted command |
| 4 | `physical_click` Send | Claude Desktop runs the command |

Step 1's tree is `slash-command-popup-1.46388.4.json`. Step 4's is
`slash-command-executed-1.46388.4.json`, and it carries the two elements that
settle the question:

```
AXHeading  "You said: /context"
AXImage    "Context window: System tools: 22.1k, 2,2 %; MCP tools: 12.8k, 1,3 %;
            System prompt: 10.3k, 1 %; Skills: 9k, 0,9 %; Other: 9.2k, 0,92 %;
            Autocompact buffer: 33k, 3,3 %"
```

The heading keeps the leading `/`, and the image is the `/context` report. The
composer executed the command. It did not send the word "context" to the model.

## How to identify the popup

The popup items are `AXMenuItem` **inside the web area**, not under `AXMenuBar`.
That distinction is the whole selector: the application's own macOS menus are
also `AXMenuItem`, and there are 226 of them.

```sh
jq -r '.[] | select(.role=="AXMenuItem") | select(.hierarchy|index("AXMenuBar")|not) | .title' \
  replaydata/agents/claudecode/desktop-evidence/slash-command-popup-1.46388.4.json
```

The macOS menu items also carry a `null` description, while every popup entry
carries an empty one — a second discriminator if the hierarchy test ever moves.

## Which commands exist

Typing a prefix filters the list, so each command's existence is a separate
measurement rather than an inference from the first 24 rows.

| Recipe asks for | Popup shows | Cells |
| --- | --- | --- |
| `/compact` | `compact` | 2-12 context-compaction |
| `/goal` | `goal` — the only match | 2-7, 2-8 autonomous-loop |
| `/cost` | `usage (cost)` — an alias of `usage` | 2-5 synchronous-slash-command |
| `/model` | `model` | 5-3 model-switch-midsession |
| `/help` | **no match** — the prefix returns `debug`, `team-onboarding`, `heapdump`, `find-skills` | 2-5 synchronous-slash-command |

`/help` is the one real absence. Cell 2-5 needs `/cost` **and** `/help`, so it
stays not-runnable — but for a missing *command*, not a missing control.

## The composer's own value is not the command

`AXValue` on the prompt field does not carry the accepted command. Measured
across the four states:

| Composer state | `AXValue` reads |
| --- | --- |
| empty, new session | `Describe a task or ask a question\n` |
| empty, inside a session | `Type / for commands\n` |
| `/context` typed | `/context` |
| after Return accepted it | `context ` |

So an empty composer reads as its own placeholder, and an accepted command loses
its slash. A `value_equals` postcondition on the accepted command must expect
`"<name> "`, never `"/<name>"`. The application's own placeholder — *Type / for
commands* — is the app advertising the feature this note had to go and measure.

## The composer rewrites a typed backtick

The composer is a rich-text editor, and TYPING is not the same as setting a
value. Typing each of these and reading the composer back:

| Typed | Composer reads |
| --- | --- |
| ``a `code` b`` | `a code␀ b` — backticks consumed, an inline code node created, a zero-width space left behind |
| `a *star* b` | `a *star* b` |
| `a _under_ b` | `a _under_ b` |
| `a **bold** b` | `a **bold** b` |
| `a #hash b` | `a #hash b` |

So the rule is the backtick, and only the backtick. It is not a display
artefact: sending ``/compact keep `echo hi` please`` headed the conversation

```
You said: /compact keep echo hi please
```

The agent received text the recipe did not write. `slash` steps therefore refuse
a backticked argument by name — the `verbatim-typed-text` limit — which is why
cell 2-7 stays not-runnable while 2-8, whose goal text has no backticks, records.

**`send` steps are unaffected.** They go through `set_value`, which writes the
accessibility value directly with no input events. Measured the same day on the
same composer: `set_value` of ``a `code` b`` reads back as ``a `code` b``, all
ten characters.

## The event source decides whether typing works at all

`type_text` carries each character in the event itself
(`keyboardSetUnicodeString`) rather than naming a key, so there is no layout to
be wrong about. That only works with a **nil** event source:

| Event | Result |
| --- | --- |
| `CGEvent(keyboardEventSource: CGEventSource(stateID: .hidSystemState), virtualKey: 0)` + unicode string | nothing reaches the app; the composer stays empty and no popup opens |
| `CGEvent(keyboardEventSource: nil, virtualKey: 0)` + unicode string | the string is typed and the popup filters |

Both were posted to `.cghidEventTap` from the same process, seconds apart.

## The key codes are US, the machine is not

The first attempt at step 1 used `keyCode 44` (`kVK_ANSI_Slash`) and typed `-`
three times. `postKeyboardEvent` posts a **physical** key code, and macOS maps it
through the active layout, which is German here:

```sh
defaults read ~/Library/Preferences/com.apple.HIToolbox.plist AppleEnabledInputSources \
  | grep 'KeyboardLayout Name'
#     "KeyboardLayout Name" = German;
```

`/` is Shift+7 on that layout. Any driver step that names a character rather
than a key must resolve the code through the active layout, or it types the
wrong character and a `value_equals` postcondition is the only thing standing
between that and a silent wrong recording.
