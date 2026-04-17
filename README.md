# punch

Direct P2P connections between any two machines, anywhere.

No server. No accounts. Just a short token you share over WhatsApp.

```
alice:  punch share
        → Token: 5pmM-Z9oL-z
        → Send this to your friend.

        Peer's reply token: _   ← paste what Bob sends back

bob:    punch join 5pmM-Z9oL-z
        → Reply token: HtRw-Lywn-x
        → Send this back to Alice.
        → Punching through NAT...
        → Connected. Direct P2P. No server.
```

## Features

- **Chat** — real-time terminal chat, no history stored anywhere
- **File transfer** — chunked send/receive with SHA-256 hash verification and a progress bar
- **Pipe** — stream stdin directly to a peer (`kubectl logs pod | punch pipe`)
- **End-to-end encrypted** — all data encrypted with ChaCha20-Poly1305 before it leaves your machine. The session key is derived from the token via HKDF-SHA256. Even if someone intercepts your packets, they see nothing.
- **Zero middleman** — if hole punching fails, punch fails loudly. No silent relay fallback, no data touches a server.
- **Single binary** — no runtime, no install, drop it anywhere on your PATH

## Security

punch is end-to-end encrypted by design, not as an afterthought.

When Alice runs `punch share`, a random 2-byte session key is generated and embedded in the token. Both Alice and Bob derive a ChaCha20-Poly1305 encryption key from it using HKDF-SHA256. Every message, file chunk, and pipe byte is encrypted before leaving the machine and decrypted on arrival.

```
token session bytes → HKDF-SHA256 → ChaCha20-Poly1305 key
```

**What this means in practice:**
- No one can read your messages or files in transit — not your ISP, not your router, not anyone on the network
- The token shared over WhatsApp is the only shared secret — keep it private
- No keys are stored anywhere, no accounts, no telemetry
- The connection is ephemeral — when both sides exit, the session is gone forever

The only data that touches any external service is a single UDP packet to Google's STUN server (`stun.l.google.com`) to discover your public IP — no content, just an IP lookup.

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

### Build from source

```bash
go install github.com/ashutoshsinghai/punch@latest
```

## Usage

### Chat

```bash
# Step 1 — Alice starts a session
punch share
# → Token: 5pmM-Z9oL-z
# → Send this token to Bob over WhatsApp/Signal

# Step 2 — Bob joins and gets a reply token
punch join 5pmM-Z9oL-z
# → Reply token: HtRw-Lywn-x
# → Bob sends this reply token back to Alice

# Step 3 — Alice enters Bob's reply token
# → Both punch through NAT simultaneously → connected
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
kubectl logs my-pod | punch pipe       # generates a token, streams stdout

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

## How it works

**Token format:** 8 bytes → ~11 base58 characters (e.g. `5pmM-Z9oL-z`)

```
[4 bytes public IPv4] [2 bytes port] [2 bytes session]
```

The port and IP come from a STUN lookup (`stun.l.google.com`) performed from the same UDP socket that will be used for the connection — so the NAT mapping is accurate.

**Two-token exchange:** Alice shares her token (IP + port + session). Bob generates a reply token with his IP + port + the same session. Both sides now know each other's public address.

**Simultaneous hole punching:** Both peers start sending UDP probe packets to each other at the same time. This opens a hole in both NATs simultaneously. Transport keepalives (every 10s) keep the hole open for the lifetime of the session.

**Encryption:** All data is encrypted with ChaCha20-Poly1305 using a key derived from the 2-byte session via HKDF-SHA256, before any packet leaves the machine.

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
