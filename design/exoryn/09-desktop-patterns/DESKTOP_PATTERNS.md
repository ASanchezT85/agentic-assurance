# Desktop Product Pattern - Windows / macOS / Linux

## Why a desktop app can justify itself
A desktop client should not merely wrap the web page. It becomes useful when it provides:
- persistent incident workspace;
- multi-window evidence/dependency inspection;
- native notifications;
- secure local credential/certificate integration;
- system tray/background presence;
- keyboard shortcuts and command palette;
- deep links;
- offline read-only cached incident/evidence snapshots where security policy permits.

## Primary pattern: Incident War Room
Three-pane layout:
1. incident queue;
2. evidence timeline/detail;
3. dependency/control/context inspector.

## Technology study
Tauri is the leading candidate for a later implementation study because it can reuse the React/TypeScript product layer while keeping a small native shell across Windows/macOS/Linux.

Do not select Tauri solely because it is lightweight; verify enterprise deployment, auto-update, certificate/keychain integration, accessibility and IT packaging requirements first.
