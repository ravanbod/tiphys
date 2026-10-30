# Tiphys — design handoff for Claude Code

Source of truth for behavior: the Tiphys spec (Claude Doc). This folder is the visual design only.
Each `*.dc.html` is one screen (static HTML + `portal.css`). Open them in a browser; links between files show the intended navigation.

## Build instructions (paste to Claude Code)
Implement the Tiphys web UI from the screens in this folder, following the spec for behavior.
- Treat `portal.css` as the design tokens/components source: IBM Plex Sans/Mono, CSS variables on `.app` (light) and `.app.dark`.
- Prod environment = amber (`.env.prod`); red only for break-glass / REC / destructive; write actions use `.btn.act`; disabled `.btn.dis`; terminal/log/replay panes always dark.
- Port screens to the project's framework as components; keep class names/tokens, replace inline SVG sprite with the project's icon approach.

## Screens and flow
Login → Main (workloads) → WorkloadDetail → Logs / ExecConnect → Terminal → ExecEnded; CronJobs; Confirms (restart / run-job dialogs); States (no-access, unreachable, running job); BreakGlass (entry, form, active banner).
Developer: MyAccess. Admin: Requests, GrantEditor, TimeWindows, Users, Teams, Clusters, Settings, Sessions. Auditor: AuditLog, Replay (+ Sessions). Mobile: static.
Personas: Sara Moradi (dev), Reza Ahmadi (admin), Leila Safavi (auditor).
