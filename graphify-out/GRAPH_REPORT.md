# Graph Report - .  (2026-07-23)

## Corpus Check
- 93 files · ~58,686 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 980 nodes · 1984 edges · 59 communities (42 shown, 17 thin omitted)
- Extraction: 88% EXTRACTED · 12% INFERRED · 0% AMBIGUOUS · INFERRED: 248 edges (avg confidence: 0.79)
- Token cost: 79,385 input · 11,063 output

## Community Hubs (Navigation)
- Binary Frame Codec (Go)
- Frontend Frame & IPC Client
- SSH Transport & Tests
- WebSocket JSON-RPC Server
- Local PTY & App Wiring
- Session Lifecycle & App
- Architecture Spine & Tooling
- Frontend Dependencies (xterm)
- Config, Log & Shell Integration
- Wterm Renderer & Types
- SSH Real Client
- Renderer Factory & Tabs
- Output Ring Buffer
- Root Package Manifest
- TypeScript Config
- Xterm Renderer
- Clipboard & OSC 52
- Runtime Package Manifest
- PTY Interface & Stub
- Tab Manager & Banner
- TerminalRenderer Interface
- Vite / Node TS Config
- Tab Manager Test Fixtures
- Wails Project Config
- Clipboard Banner Impl
- Clipboard Access Adapters
- SSH Error Types
- Wails Runtime Types
- Prettier Config
- Wails App Entrypoint
- Pre-commit Hook
- E2E CI & Wails Shell
- Beads Sync Hook
- MCP Test Server Config
- Wails Event Subscriptions
- Glyph Probe Script
- Post-checkout Hook
- Post-merge Hook
- Pre-commit Hook (git)
- Pre-push Hook (git)
- Prepare-commit-msg Hook
- Dependency Injection Paradigm
- Pre-push Hook (.githooks)
- Frontend CI Job
- AD-2 Go Backend Core
- Config Module Seam
- nocx Go Module Path

## God Nodes (most connected - your core abstractions)
1. `NewSlogAdapter()` - 69 edges
2. `NewWSServer()` - 38 edges
3. `connectWS()` - 37 edges
4. `Logger` - 34 edges
5. `WSServer` - 32 edges
6. `jsonrpcCallWithID()` - 31 edges
7. `WSClient` - 28 edges
8. `newRegWithStub()` - 28 edges
9. `ID` - 26 edges
10. `TerminalRenderer` - 24 edges

## Surprising Connections (you probably didn't know these)
- `Frontend app shell (index.html: #app, #tabbar, #panes → main.ts)` --references--> `terminal frontend module (xterm.js render state, OSC parsing)`  [INFERRED]
  frontend/index.html → docs/architecture.md
- `WailsApp` --references--> `App`  [EXTRACTED]
  main.go → internal/app/app.go
- `Frontend app shell (index.html: #app, #tabbar, #panes → main.ts)` --references--> `ui frontend module (tabs, menus, config)`  [INFERRED]
  frontend/index.html → docs/architecture.md
- `CI backend job (macos-latest; gofumpt, golangci-lint, go test -race)` --cites--> `docs/architecture.md — nocx High-Level Architecture`  [EXTRACTED]
  .github/workflows/ci.yml → docs/architecture.md
- `App` --references--> `ShellIntegration`  [EXTRACTED]
  internal/app/app.go → internal/shellintegration/shellintegration.go

## Import Cycles
- None detected.

## Hyperedges (group relationships)
- **Architecture spine — binding invariants AD-1..AD-10** — docs_architecture_ad_1, docs_architecture_ad_2, docs_architecture_ad_3, docs_architecture_ad_4, docs_architecture_ad_5, docs_architecture_ad_6, docs_architecture_ad_7, docs_architecture_ad_8, docs_architecture_ad_9, docs_architecture_ad_10 [EXTRACTED 1.00]
- **Go core (one binary, multi-target) backend modules** — docs_architecture_pty, docs_architecture_ssh, docs_architecture_session, docs_architecture_transport, docs_architecture_config, docs_architecture_shellintegration [EXTRACTED 1.00]
- **Switchable renderers behind TerminalRenderer interface** — docs_decisions_0001_xterm_js_as_vt_frontend_xterm_js, docs_decisions_0001_xterm_js_as_vt_frontend_ghostty_web, docs_decisions_0001_xterm_js_as_vt_frontend_wterm [INFERRED 0.85]

## Communities (59 total, 17 thin omitted)

### Community 0 - "Binary Frame Codec (Go)"
Cohesion: 0.12
Nodes (69): Duration, NewSlogAdapter(), IDToBytes(), DecodeFrame(), T, TestDecodeFrameAtMinimumSize(), TestDecodeFrameBadMsgType(), TestDecodeFrameBadVersion() (+61 more)

### Community 1 - "Frontend Frame & IPC Client"
Cohesion: 0.06
Nodes (19): DecodedFrame, decodeFrame(), encodeFrame(), hexToBytes(), isSessionID(), AckThrottle, AttachResult, ControlMessage (+11 more)

### Community 3 - "SSH Transport & Tests"
Cohesion: 0.10
Nodes (46): Context, ReadWriteCloser, NewStub(), NewStubChannel(), NewReal(), generateSigner(), Listener, Mutex (+38 more)

### Community 4 - "WebSocket JSON-RPC Server"
Cohesion: 0.11
Nodes (29): Conn, Context, Listener, Mutex, Once, RawMessage, Request, isJSONObject() (+21 more)

### Community 5 - "Local PTY & App Wiring"
Cohesion: 0.06
Nodes (41): localPTYFactory, Cmd, File, Config, New(), T, TestNew(), TestNew_AllModulesInjected() (+33 more)

### Community 6 - "Session Lifecycle & App"
Cohesion: 0.08
Nodes (35): App, Context, Stub, abbreviateHome(), Context, Mutex, Once, IDFromBytes() (+27 more)

### Community 7 - "Architecture Spine & Tooling"
Cohesion: 0.07
Nodes (49): .beads/config.yaml — bd repo settings (JSONL auto-export, Dolt sync remote), Beads (bd) — AI-native, git-native issue tracker, Dolt database (versioned issue store; refs/dolt/data sync), CI backend job (macos-latest; gofumpt, golangci-lint, go test -race), CI pipeline (GitHub Actions; release/** + v* tags + dispatch), golangci-lint config (v1.64.8 schema; gofmt, govet, gosec, staticcheck…), AGENTS.md — working rules for AI agents on nocx, CLAUDE.md — pointer to AGENTS.md and sources of truth (+41 more)

### Community 8 - "Frontend Dependencies (xterm)"
Cohesion: 0.04
Nodes (45): eslint, eslint-config-prettier, dependencies, @wterm/dom, @xterm/addon-canvas, @xterm/addon-fit, @xterm/addon-unicode11, @xterm/addon-webgl (+37 more)

### Community 9 - "Config, Log & Shell Integration"
Cohesion: 0.07
Nodes (25): Config, Stub, NewStub(), Context, T, TestNewSlogAdapter_DoesNotPanic(), TestSlogAdapter_With(), TestSlogAdapter_WithContext() (+17 more)

### Community 10 - "Wterm Renderer & Types"
Cohesion: 0.08
Nodes (12): RFC-3986, CwdCallback, CwdEvent, DataCallback, ResizeCallback, TitleCallback, WtermRenderer, BellCallback (+4 more)

### Community 11 - "SSH Real Client"
Cohesion: 0.10
Nodes (19): Client, HostKeyCallback, buildTerminalModes(), currentUser(), expandPath(), AuthMethod, Config, Context (+11 more)

### Community 12 - "Renderer Factory & Tabs"
Cohesion: 0.15
Nodes (10): ADR-0001, AgentStatus, detectAgentStatus(), createRenderer(), resolveRendererName(), cwdTooltip(), DEFAULT_RENDERER, directoryLabel() (+2 more)

### Community 13 - "Output Ring Buffer"
Cohesion: 0.16
Nodes (9): Context, Mutex, newOutputRing(), T, TestOutputRing_CancellableWaitForData(), TestOutputRing_WaitForDataAlreadyCancelled(), TestOutputRing_WaitForDataClosedRing(), TestOutputRing_WakeBroadcasts() (+1 more)

### Community 14 - "Root Package Manifest"
Cohesion: 0.09
Nodes (21): author, bugs, url, description, devDependencies, @playwright/test, directories, doc (+13 more)

### Community 15 - "TypeScript Config"
Cohesion: 0.10
Nodes (20): compilerOptions, esModuleInterop, forceConsistentCasingInFileNames, isolatedModules, jsx, lib, module, moduleResolution (+12 more)

### Community 17 - "Clipboard & OSC 52"
Cohesion: 0.17
Nodes (9): ClipboardGate, createClipboardAccess(), decodeOsc52(), shouldCopy(), WailsClipboard, main(), GetWSPort(), ClipboardGetText() (+1 more)

### Community 18 - "Runtime Package Manifest"
Cohesion: 0.11
Nodes (18): author, bugs, url, description, homepage, keywords, license, main (+10 more)

### Community 19 - "PTY Interface & Stub"
Cohesion: 0.18
Nodes (11): Context, NewStub(), T, TestStub_Close(), TestStub_DoneOpenBeforeClose(), TestStub_ImplementsInterface(), TestStub_Read_ReturnsEOF(), TestStub_Resize() (+3 more)

### Community 20 - "Tab Manager & Banner"
Cohesion: 0.24
Nodes (4): ClipboardBanner, RendererName, TabManager, BannerFake

### Community 22 - "Vite / Node TS Config"
Cohesion: 0.13
Nodes (14): compilerOptions, esModuleInterop, forceConsistentCasingInFileNames, isolatedModules, module, moduleResolution, noEmit, resolveJsonModule (+6 more)

### Community 23 - "Tab Manager Test Fixtures"
Cohesion: 0.28
Nodes (10): ClientFake, createRendererMock(), makeBanner(), makeClient(), makeClipboard(), makeSession(), mountTabManager(), resetSessionCounter() (+2 more)

### Community 24 - "Wails Project Config"
Cohesion: 0.18
Nodes (10): author, email, name, frontend:build, frontend:dev:serverUrl, frontend:dev:watcher, frontend:install, name (+2 more)

### Community 26 - "Clipboard Access Adapters"
Cohesion: 0.24
Nodes (4): BrowserClipboard, ClipboardAccess, NoopClipboard, ClipboardFake

### Community 27 - "SSH Error Types"
Cohesion: 0.20
Nodes (4): ErrAuthFailed, ErrEncryptedKey, ErrHostKeyMismatch, ErrUnknownHostKey

### Community 28 - "Wails Runtime Types"
Cohesion: 0.25
Nodes (7): EnvironmentInfo, NotificationAction, NotificationCategory, NotificationOptions, Position, Screen, Size

### Community 29 - "Prettier Config"
Cohesion: 0.29
Nodes (6): arrowParens, printWidth, semi, singleQuote, tabWidth, trailingComma

### Community 31 - "Pre-commit Hook"
Cohesion: 0.80
Nodes (4): pre-commit script, check_cmd(), ok(), warn()

### Community 32 - "E2E CI & Wails Shell"
Cohesion: 0.50
Nodes (4): CI e2e job (macos-latest; Wails app + Playwright), Playwright (e2e harness; chromium + webkit against :34115), AD-3: Wails v2 as the MVP desktop shell, Wails v2 desktop shell (WKWebView, embeds backend)

### Community 34 - "MCP Test Server Config"
Cohesion: 0.50
Nodes (3): npx, playwright-test, run-test-mcp-server

### Community 35 - "Wails Event Subscriptions"
Cohesion: 0.67
Nodes (3): EventsOn(), EventsOnce(), EventsOnMultiple()

## Knowledge Gaps
- **151 isolated node(s):** `beads-hook.sh script`, `npx`, `run-test-mcp-server`, `semi`, `singleQuote` (+146 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **17 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `Logger` connect `Config, Log & Shell Integration` to `Binary Frame Codec (Go)`, `SSH Transport & Tests`, `WebSocket JSON-RPC Server`, `Local PTY & App Wiring`, `Session Lifecycle & App`, `SSH Real Client`, `PTY Interface & Stub`?**
  _High betweenness centrality (0.082) - this node is a cross-community bridge._
- **Why does `NewSlogAdapter()` connect `Binary Frame Codec (Go)` to `SSH Transport & Tests`, `Local PTY & App Wiring`, `Session Lifecycle & App`, `Config, Log & Shell Integration`, `PTY Interface & Stub`?**
  _High betweenness centrality (0.050) - this node is a cross-community bridge._
- **Why does `WSServer` connect `WebSocket JSON-RPC Server` to `Binary Frame Codec (Go)`, `Config, Log & Shell Integration`, `Session Lifecycle & App`?**
  _High betweenness centrality (0.031) - this node is a cross-community bridge._
- **Are the 66 inferred relationships involving `NewSlogAdapter()` (e.g. with `New()` and `TestNewSlogAdapter_DoesNotPanic()`) actually correct?**
  _`NewSlogAdapter()` has 66 INFERRED edges - model-reasoned connections that need verification._
- **Are the 34 inferred relationships involving `NewWSServer()` (e.g. with `New()` and `TestWSServer_AckTrimsRing()`) actually correct?**
  _`NewWSServer()` has 34 INFERRED edges - model-reasoned connections that need verification._
- **What connects `beads-hook.sh script`, `npx`, `run-test-mcp-server` to the rest of the system?**
  _151 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `Binary Frame Codec (Go)` be split into smaller, more focused modules?**
  _Cohesion score 0.12216216216216216 - nodes in this community are weakly interconnected._