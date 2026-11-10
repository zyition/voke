<p align="center">
  <img src="assets/voke-icon.svg" alt="Voke" width="128">
</p>

<h1 align="center">Voke</h1>

<p align="center">
  <strong>English</strong> |
  <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <a href="https://awesomemygo.dev/apps/voke"><img src="https://awesomemygo.dev/badge.svg" alt="Listed on Awesome MyGo"></a>
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue" alt="License: MIT"></a>
  <a href="https://github.com/zyition/voke/actions/workflows/ci.yml"><img src="https://github.com/zyition/voke/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/language-Go-00ADD8" alt="Language: Go"></a>
  <a href="https://www.microsoft.com/windows"><img src="https://img.shields.io/badge/platform-Windows-0078D6" alt="Platform: Windows"></a>
</p>

<p align="center">
  <strong>Voke</strong> (pronounced /voʊk/, the spirit of <em>invoke</em>) is a native, keyboard-first launcher for Windows, similar to PowerToys Run.<br>
  It is built with Go and <a href="https://github.com/egoist/mygo">mygo</a>: a single executable with no .NET, WebView, or external runtime, living in the system tray.
</p>

Why it exists: most launchers in this space use Electron or WebView, which cost more memory and start slower. Voke calls Win32 / COM directly from Go and renders through mygo's D3D11 backend. After startup, PrivateCommit is roughly 55 MB, and about 110 MB once the app index is built; most of that is the graphics stack and native system enumeration (the Go heap is only a few MB). The settings window is destroyed on close, icon caches are bounded, and memory does not keep growing with repeated use.

## Highlights

- Global hotkey to show/hide the panel (default `Alt+Space`, record a new one in Settings).
- Each plugin can have its own global shortcut (clipboard ships with ``Alt+` ``; all others are unbound by default and can be recorded in Settings). The shortcut jumps straight to that plugin.
- Pasting into elevated windows and opening terminal profiles as administrator trigger UAC only when needed: a temporary helper process does the work, so no high-privilege process stays resident.
- 12 built-in plugins: Apps, Windows, Keep Awake, Terminal, VS Code Workspaces, Zed Workspaces, Calculator, Timestamp, Base64, UUID, Random, and Clipboard History.
- Type `#` for the plugin list, filtered by name, pinyin, or English alias. Plugins can have second-level interactions (Base64 encode/decode, regenerate random values, and so on).
- Tray resident: right-click opens the menu (the first item is a live Keep Awake status), double-click shows the panel.
- Launch at login (writes `HKCU\...\Run`), dark mode that follows the system, single instance (launching again reveals the existing instance).
- Pinyin / fuzzy matching: typing pinyin initials matches apps without switching IMEs.
- UI in English or Simplified Chinese, following the system by default. Change it in Settings under General → Language.

## Quick start

### Download and install

Two release options:

- **Installer** (`voke-setup-<version>.exe`): per-user install to
  `%LOCALAPPDATA%\Programs\Voke`; upgrading ends any running tray instance automatically.
- **Portable** (`voke-vX.Y.Z-windows-x86_64.zip`): unzip and run `voke.exe` from any folder.

Launch at login is controlled by the switch in Settings (it writes/deletes
`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`). The installer itself does not add a startup entry.

### Configuration

The configuration file lives at `%APPDATA%\voke\config.toml`:

- If the file is missing, all defaults apply. You can write only the keys you care about; unspecified keys keep their defaults.
- Settings can also be edited visually (`Ctrl+,` or the tray menu): hotkey, language, theme, and each plugin's enabled / global / group order (`weight`) / prefix / activation / global shortcut. Changes apply immediately; there is no Save button. When a plugin is disabled, its remaining settings are disabled too.
- The top-level `language` key accepts `"system"` (default), `"zh"`, or `"en"`. `"system"` follows the Windows UI language and falls back to English for non-Chinese systems.
- Plugin shortcuts are written under `[providers.<id>]` as `hotkey` (for example ``hotkey = "Alt+`"``). Clipboard ships with ``Alt+` ``; all other plugins are unbound by default. Only **enabled** plugins register their shortcut, and disabling one unregisters it immediately. Duplicates with the main hotkey or another enabled plugin are reported inline and are not saved. A combination may also be reserved by the system or another app; if Voke reports it as taken, choose another one (for example, ``Alt+` `` is already used by some tools on some machines).
- Prefix conflicts, whitespace, or reserved letter/digit prefixes are reported inline and are not saved.
- Clipboard history size is edited in the Clipboard plugin card, or by writing `[clipboard] max_entries` (default 100, range 1–1000). Up to 10 pinned items do not count toward the limit. History is memory-only and is cleared on exit.
- Random string types are driven by **character sets**: the Random plugin card lets you add, remove and edit a name, pattern, default length and enabled switch. Names are unique within the plugin and case-sensitive; patterns are interpreted as **character-class contents** such as `a-zA-Z0-9`, `a-f0-9`, `[A-F0-9]`, and may contain literal non-ASCII characters such as `￥` or `…`. The plugin-level default length applies to every character set without its own length (default 16); set a character-set length to 0 to follow it. Defaults are letters + digits, letters + digits + symbols (`!@#$%^&*-_=+`), and lowercase hex (`0-9a-f`, 32 chars by default). You can also write them directly:

  ```toml
  [providers.random]
  default_length = 16

  [[providers.random.charsets]]
  name = "uppercase hex"
  pattern = "A-F0-9"
  length = 32
  enabled = true
  ```

## Keyboard

| Key | Action |
|---|---|
| `Alt+Space` | Show / hide the panel (toggle; configurable in Settings) |
| Plugin shortcut | User-recorded per plugin; reveals the panel directly in that plugin, or focuses it if already there. It never hides the panel. Unbound by default except clipboard's ``Alt+` `` |
| `↑` / `↓` | Move the selection up / down |
| `Ctrl+N` / `Ctrl+P` | Move down / up (same as `↓` / `↑`) |
| `Enter` | Run the selected item |
| `Esc` | Hide the panel (it also hides on focus loss) |
| `Backspace` | Clear the current query first, then step back through plugin / sub-mode levels |
| `#` | Open the plugin picker (filter by name, pinyin, or English alias) |
| `Ctrl+,` | Open Settings |
| `Ctrl+Delete` | Delete the selected clipboard item; with a window result, close that window (same as the close button, so the app may ask to save) |
| `Ctrl+Shift+Delete` | With a window result, end the owning process immediately (tray / background apps exit too; unsaved data is lost) |
| Tray double-click | Show the panel (right-click opens the menu) |

Inside Clipboard History (`@c`):

| Key | Action |
|---|---|
| `Enter` | Paste with the original format into the window that was focused before the panel opened (terminals use `Shift+Insert`) |
| `Ctrl+Enter` | Paste as plain text (not available for image-only items) |
| `Ctrl+Shift+T` | Pin / unpin (up to 10 pinned items, excluded from the regular limit) |
| `Ctrl+Delete` | Delete the current item |

If the paste target is an elevated window, UIPI silently blocks normal input injection; Voke shows one UAC prompt and a temporary elevated helper process sends the paste keys. If UAC is canceled or the helper fails, the content is already on the clipboard, and the panel returns with a hint to paste manually.

Inside Terminal Profiles (`terminal`):

| Key | Action |
|---|---|
| `Enter` | Open the profile in Windows Terminal |
| `Ctrl+Enter` | Open the profile as administrator (UAC) |

## Plugins

There are 12 built-in plugins. Three participate in global search: `apps` (no prefix),
`calculator` (`=`), and `windows` (`<`); the rest are reached through their prefix. Indirect
prefixes require the prefix plus one space.

| id | What it does | Default trigger |
|---|---|---|
| `apps` | Launch Start menu apps (desktop shortcuts + packaged apps) | none (global) |
| `windows` | Switch to an open window; close it or end its task from the row actions | `<` (Direct, also global) |
| `calculator` | Evaluate arithmetic expressions | `=` (Direct, also global) |
| `awake` | Keep the system awake, optionally with the screen | `@a` (Direct) |
| `terminal` | Open a Windows Terminal profile; `Ctrl+Enter` opens as administrator | `_` (Direct) |
| `vscode` | Recent VS Code workspaces, opened in VS Code | `{` (Direct) |
| `zed` | Recent Zed workspaces, opened in Zed | `}` (Direct) |
| `timestamp` | Convert between timestamps and readable time | `@t` + space (Indirect) |
| `base64` | Base64 encode / decode | `@b` + space (Indirect) |
| `uuid` | Generate UUID v4 values | `@u` + space (Indirect) |
| `random` | Random integers / strings with configurable character sets and default length, with regenerate | `@r` + space (Indirect) |
| `clipboard` | Clipboard history (text / HTML / RTF / images; paste, plain-text paste, pin, delete) | `@c` (Direct), default shortcut ``Alt+` `` |

> `#` is reserved for the plugin picker. Plugin prefixes live under the `@` namespace to avoid a
> short prefix shadowing a longer one. Settings rejects prefixes that are prefixes of each other;
> legacy `#b/#u/#t/#r` values are migrated to the new defaults on load.
>
> Plugin aliases (such as `b` / `b64`) are built-in metadata used by picker filtering; they are not
> edited in the config file. The full provider contract and default configuration live in
> [`docs/architecture.md`](docs/architecture.md).

## Build and develop

Full environment setup, layering rules, and verification commands are in [`CONTRIBUTING.md`](CONTRIBUTING.md) (Chinese). Common commands:

```bash
gofmt -l . && go vet ./... && go test -race -count=1 -shuffle=on ./...

GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -tags mygo_noinspector -ldflags="-s -w -H windowsgui" -o /tmp/voke.exe .
```

- Artifact: `/tmp/voke.exe` (cross build). The installer is built with `bash packaging/build-installer.sh`,
  producing `packaging/dist/voke-setup-*.exe` (per-user install into `%LOCALAPPDATA%\Programs\Voke`).
- Release: pushing a `vX.Y.Z` tag triggers `.github/workflows/release.yml`, builds the Go executable on
  windows-latest, injects the version, produces the installer and portable zip, and creates a GitHub
  Release. The tag is the single source of truth for the version.

Tech stack: Go (version in `go.mod`), mygo (D3D11 / DirectWrite), `go-pinyin`, a Go port of
nucleo-matcher scoring, SQLite (read-only Zed / VS Code history), and Inno Setup 7.1 for packaging.
The Rust/gpui implementation is archived on the `legacy-rust` branch and `v0.1.x` tags, outside the
current tree.

## Architecture

In one sentence: pure logic / providers / platform / UI / assembly layers; logic and providers are
testable natively on Linux, while mygo platform capabilities and Win32 calls are confined to
`internal/platform`.

| Directory | Responsibility | Constraint |
|---|---|---|
| `internal/logic` | Engine, config, panel state, search/frequency, icon cache, logging | Pure Go, no mygo / syscall |
| `internal/match` | Pinyin search keys + nucleo fuzzy scoring | Pure Go |
| `internal/providers` | Provider contract and 11 plugin implementations | Query-only; side effects returned as Actions |
| `internal/platform` | mygo platform modules / Win32 / COM boundary | The only layer that wraps platform capabilities |
| `internal/ui` | mygo components, theme, panel/settings views | Immediate mode |
| `internal/app` | Assembly, lifecycle, main-thread state and execution | Main-thread constraints |

See [`docs/architecture.md`](docs/architecture.md) for details (Chinese).

## Contributing

- Commit messages use **Conventional Commits with an English description** (for example, `feat: add pinyin-initial fuzzy matching`).
  Bilingual subjects are allowed as `feat: add X（新增 X）`.
  Common types: `feat` / `fix` / `perf` / `refactor` / `docs` / `test` / `chore`.
- **CHANGELOG is generated by [git-cliff](https://github.com/orhun/git-cliff) in English; do not edit `CHANGELOG.md` by hand.**
  Entries up to v0.6.0 remain in Chinese.
- CI runs `gofmt`, a `go mod tidy` check, `go vet`, `go test -race`, and a Windows cross build; make sure
  they pass locally before committing.
- More layering rules and prohibited practices are in [`CONTRIBUTING.md`](CONTRIBUTING.md) (Chinese).

## Security

Voke does not access the network and does not report any data (no telemetry, no backend). See
[`SECURITY.md`](SECURITY.md) for details on auto-start, launching external programs, and configuration
I/O, as well as the vulnerability disclosure process. Please do not open a public issue for security
reports.

## License

[MIT](LICENSE).

## Documentation

- [`docs/architecture.md`](docs/architecture.md) — current Go + mygo architecture, query flow, provider contract, configuration
- [`docs/KNOWN-ISSUES.md`](docs/KNOWN-ISSUES.md) — current limitations and expected behavior
- [`docs/BACKLOG.md`](docs/BACKLOG.md) — TODO, product decisions, and future work
- [`docs/notes/memory.md`](docs/notes/memory.md) — memory measurements and implemented optimizations
- [`docs/notes/matching.md`](docs/notes/matching.md) — matching / pinyin consistency
- [`docs/notes/ranking.md`](docs/notes/ranking.md) — ranking score: quality tiers and the bounded frequency bonus
- [`docs/notes/rendering.md`](docs/notes/rendering.md) — main-thread frame budget: what must stay out of the render frame, and how to measure it
- [`docs/legacy/README.md`](docs/legacy/README.md) — archive of the Rust/gpui era and the Go PoC stage
- [`AGENTS.md`](AGENTS.md) — repository notes for AI agents
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — contribution guide
- [`SECURITY.md`](SECURITY.md) — security and vulnerability disclosure
