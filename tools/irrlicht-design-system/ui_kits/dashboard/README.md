# Dashboard UI Kit

Recreation of the Irrlicht web dashboard at `platforms/web/index.html`. This is the dense, data-rich face of the product — the full session list, the menu-bar window equivalent.

## Components

- `Header.jsx` — sticky top bar with logo, state summary, WebSocket status ring
- `SessionRow.jsx` — one row per agent; state icon, badges, branch, pressure bar, cost, model, elapsed, short-id
- `GroupHeader.jsx` — collapsible project group with aggregate cost and agent count
- `PressureAlert` (defined inside `GroupHeader.jsx`) — "switch to a fresh session soon" warning
- `GasTown.jsx` — orchestrator section: global agents, rigs (codebases), convoys
- Dashboard / Raw tab bar — inline markup in `index.html`'s `App()`, not a separate component file

## Source of truth

Colors, spacing, and markup patterns lifted directly from `platforms/web/index.html` in `ingo-eichhorst/Irrlicht`.

Open `index.html` to see the kit assembled as a live dashboard with fake data.
