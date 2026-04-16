# punch

Direct P2P connections between any two machines, anywhere.

No server. No accounts. Just a short token you share over WhatsApp.

```
alice:  punch share
        → Token: 5pmM-Z67o-HtRw-Lywn-z
        → Send this to your friend.

bob:    punch join 5pmM-Z67o-HtRw-Lywn-z
        → Punching through NAT...
        → Connected to alice. Direct P2P. No server.
```

## Features

- **Chat** — real-time encrypted terminal chat, no history stored anywhere
- **File transfer** — chunked send/receive with SHA-256 hash verification and a progress bar
- **Pipe** — stream stdin directly to a peer (`kubectl logs pod | punch pipe`)
- **Zero middleman** — if hole punching fails, punch fails loudly. No silent relay fallback.
- **End-to-end encrypted** — ChaCha20-Poly1305, key derived from the session token
- **Expiring tokens** — 10 minutes by default, replayed tokens rejected
- **Single binary** — no runtime, no install, drop it anywhere on your PATH

## Installation

### macOS / Linux

```bash
curl -sSL https://raw.githubusercontent.com/ashutoshsinghai/punch/main/scripts/install.sh | sh
```

Installs to `/usr/local/bin` (or `~/bin` if no write access). Works on Intel and Apple Silicon.

---

### Windows — PowerShell

```powershell
irm https://raw.githubusercontent.com/ashutoshsinghai/punch/main/scripts/install.ps1 | iex
```

Installs to `%USERPROFILE%\bin` and adds it to your PATH automatically. No admin needed.

---

### Windows — Command Prompt (cmd.exe)

Windows 10 and 11 have `curl` and `tar` built in:

```cmd
curl -L -o punch.zip https://github.com/ashutoshsinghai/punch/releases/latest/download/punch_windows_amd64.zip
tar -xf punch.zip
mkdir %USERPROFILE%\bin
move punch.exe %USERPROFILE%\bin\
del punch.zip
```

Then add `%USERPROFILE%\bin` to your PATH:

```cmd
setx PATH "%PATH%;%USERPROFILE%\bin"
```

Restart your terminal and run `punch version` to verify.

---

### Manual download (any platform)

Download the right binary for your OS from [GitHub Releases](https://github.com/ashutoshsinghai/punch/releases), extract it, and move it somewhere on your PATH.

| OS | File |
|---|---|
| macOS Apple Silicon | `punch_darwin_arm64.tar.gz` |
| macOS Intel | `punch_darwin_amd64.tar.gz` |
| Linux ARM64 | `punch_linux_arm64.tar.gz` |
| Linux x86-64 | `punch_linux_amd64.tar.gz` |
| Windows x86-64 | `punch_windows_amd64.zip` |

---

### Build from source (requires Go 1.21+)

```bash
go install github.com/ashutoshsinghai/punch@latest
```

## Usage

### Chat

```bash
# Alice starts a session
punch share

# Bob connects
punch join <token>
```

Type messages and hit Enter. `/quit` or Ctrl+C to exit.

### File transfer

```bash
# Option A — Bob sends to Alice
punch share                        # Alice waits, prints token
punch send report.pdf <token>      # Bob sends

# Option B — Alice sends to Bob
punch receive                      # Alice listens, prints token
punch send report.pdf <token>      # Bob sends (token from Alice)
```

### Pipe

```bash
# Stream anything to a peer
kubectl logs my-pod | punch pipe       # generates a token, stream stdout

punch pipe <token> > output.txt        # peer receives and writes to file
```

### All commands

| Command | Description |
|---|---|
| `punch share` | Start a session, print a token, open chat |
| `punch join <token>` | Connect to a peer, open chat |
| `punch send <file> <token>` | Send a file directly to a peer |
| `punch receive [token]` | Receive a file (generates a token if none given) |
| `punch pipe [token]` | Pipe stdin to peer, or receive a pipe to stdout |
| `punch version` | Show current version |
| `punch upgrade` | Upgrade to the latest version |
| `punch help` | Show usage |

### Flags

```bash
punch share --expire 30m        # custom token expiry (default 10m)
punch share --token base58      # compact token format instead of word groups
```

## How it works

The token encodes Alice's public IP, local IP, port, a random session ID, and an expiry timestamp — all in 18 binary bytes (~25 base58 characters).

Bob decodes the token and sends UDP probe packets to Alice's address. This opens a "hole" in both NATs simultaneously. Alice replies the moment she receives Bob's first probe. Both holes are open — direct UDP traffic flows.

All data is encrypted with ChaCha20-Poly1305 using a key derived from the session ID via HKDF-SHA256.

### NAT compatibility

| NAT type | Common where | Works? |
|---|---|---|
| Full cone | Home routers | Yes |
| Address-restricted | Cafes, most home | Yes |
| Port-restricted | Some corporate | Yes |
| Symmetric | Strict corporate, 4G | No |

When it fails, punch tells you clearly and exits. No silent relay fallback.

## License

MIT
