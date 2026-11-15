module github.com/shady2k/nocx

go 1.26

toolchain go1.26.5

require (
	// TEMPORARY: pin upstream PR #215 (9246436fffe85773950f49dcc73de333da5212e6).
	// It fixes the lost ioctl pointer/index from issue #216; drop this pin when
	// a tagged release carries the fix. SyscallConn.Control also preserves nonblocking.
	github.com/creack/pty v1.1.25-0.20260601142114-9246436fffe8
	github.com/ebitengine/purego v0.9.1
	github.com/godbus/dbus/v5 v5.2.2
	github.com/gorilla/websocket v1.5.3
	github.com/ncruces/go-sqlite3 v0.35.2
	github.com/pkg/sftp v1.13.11
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	github.com/wailsapp/wails/v3 v3.0.0-beta.9
	github.com/zalando/go-keyring v0.2.8
	golang.org/x/crypto v0.54.0
	golang.org/x/mod v0.37.0
	golang.org/x/sys v0.47.0
	gopkg.in/yaml.v3 v3.0.1
)

require golang.org/x/tools v0.47.0

require (
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/niemeyer/pretty v0.0.0-20200227124842-a10e7caefd8e // indirect
	golang.org/x/sync v0.22.0 // indirect
	gopkg.in/check.v1 v1.0.0-20200227125254-8fa46927fb4f // indirect
)

require (
	git.sr.ht/~jackmordaunt/go-toast/v2 v2.0.3 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/google/uuid v1.6.0
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-sqlite3-wasm/v3 v3.2.35303 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/sirupsen/logrus v1.9.3
	golang.org/x/net v0.56.0
	golang.org/x/text v0.40.0 // indirect
	lukechampine.com/adiantum v1.1.1 // indirect
)
