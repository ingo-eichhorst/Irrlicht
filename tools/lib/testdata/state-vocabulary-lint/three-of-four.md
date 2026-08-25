# The mutation fixture

This file exists to be caught. The line below names three of the four
canonical states and omits the fourth — the exact shape that was correct
under the old vocabulary and is silently stale under the current one.

A session is always in one of `working`, `waiting` or `ready`.

Nothing else in this file matters; the gate reads one line at a time.
