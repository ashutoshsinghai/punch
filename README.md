# punch

**Direct P2P chat and file transfer between any two machines — no server, no accounts, no relay.**

Exchange a short token over WhatsApp or Signal. That's it.

```
alice:  punch share
        Token: swift-comet — olive-TIGER-seven
        Send this to your peer over WhatsApp/Signal.

        Peer's reply token: _   ← paste what Bob sends back

bob:    punch join swift-comet — olive-TIGER-seven
        Reply token: frozen-hawk — nine-PRISM-delta
        Send this back to Alice.
        → Connected. Direct P2P. No server.
```

---

## The honest P2P story

Most apps that claim "peer-to-peer" are quietly routing your data through their relay servers (TURN) when a direct connection isn't possible. Google Meet, Discord, Zoom — they all do this. The P2P part is just an optimisation to save them bandwidth costs.

**punch is different:** it attempts a direct connection and tells you clearly when it can't establish one. No silent relay fallback. No data touches a server mid-transfer. If it fails, it tells you exactly why and what to do.

**When it works:** both peers on typical home routers, one or both on a mobile hotspot, or one side on a server with a public IP.

**When it won't work:** some ISPs (common in India — Jio, Airtel, BSNL) run stateful firewalls above the home router that block UDP traffic between two residential IPs. Your internet still works fine because normal browsing is always *you* initiating to a server. Direct residential-to-residential UDP looks different to their firewall. Running `punch check` before connecting tells you upfront if your network is likely to succeed.

---

## Installation

### macOS / Linux

```bash
curl -sSL https://raw.githubusercontent.com/ashutoshsinghai/punch/main/scripts/install.sh | sh
```

Installs to `/usr/local/bin` (or `~/bin` if no write access). Works on Intel and Apple Silicon.

### Windows — PowerShell

```powershell
irm https://raw.githubusercontent.com/ashutoshsinghai/punch/main/scripts/install.ps1 | iex
```

Installs to `%USERPROFILE%\bin` and adds it to your PATH automatically. No admin needed.

### Windows — Command Prompt

```cmd
curl -L -o punch.zip https://github.com/ashutoshsinghai/punch/releases/latest/download/punch_windows_amd64.zip
tar -xf punch.zip
mkdir %USERPROFILE%\bin
move punch.exe %USERPROFILE%\bin\
del punch.zip
setx PATH "%PATH%;%USERPROFILE%\bin"
```

Restart your terminal and run `punch version` to verify.

### Manual download

Download the right binary from [GitHub Releases](https://github.com/ashutoshsinghai/punch/releases):

| Platform | File |
|---|---|
| macOS Apple Silicon | `punch_darwin_arm64.tar.gz` |
| macOS Intel | `punch_darwin_amd64.tar.gz` |
| Linux ARM64 | `punch_linux_arm64.tar.gz` |
| Linux x86-64 | `punch_linux_amd64.tar.gz` |
| Windows x86-64 | `punch_windows_amd64.zip` |

### Build from source

```bash
go install github.com/ashutoshsinghai/punch@latest
```

---

## Usage

### Before you start — check your network

Run this before sharing a token. It tells you whether your network supports hole punching:

```
$ punch check

[ ✓ ] STUN — your public address: 116.75.69.67:52203
[ ✓ ] NAT type — port-restricted, hole punching should work
[ ✓ ] verdict — your side is ready
```

If you see warnings, the output explains exactly why and what to try (usually: switch to a mobile hotspot).

---

### Chat

```bash
# Alice starts a session
punch share
# Token: swift-comet — olive-TIGER-seven
# Send this to Bob over WhatsApp/Signal

# Bob joins with Alice's token
punch join swift-comet — olive-TIGER-seven
# Reply token: frozen-hawk — nine-PRISM-delta
# Bob sends this reply token back to Alice

# Alice pastes Bob's reply token → both punch through NAT → connected
```

Both sides get a random display name (e.g. `swift-comet`) at session start. Use `/rename <name>` to change yours — your peer sees the update instantly.

---

### In-chat commands

Once connected, type these in the chat prompt:

| Command | Description |
|---|---|
| `/send <file>` | Send a file to peer (Selective Repeat ARQ, SHA-256 verified) |
| `/qsend <file>` | Send a file via QUIC (faster for large files) |
| `/ping` | Measure round-trip time to peer |
| `/ip` / `/info` | Show your and peer's public IP address |
| `/geo` | Look up your peer's approximate location |
| `/rename <name>` | Change your display name for this session |
| `/ls` | List files in your current directory |
| `/clear` | Clear the chat window |
| `/help` | Show all commands |
| `/quit` | Exit punch |

---

### Standalone file transfer

If you just need to transfer a file without opening a chat session:

```bash
# Alice receives (prints a token for the sender)
punch receive <token>

# Bob sends
punch send report.pdf <token>
```

QUIC variants for benchmarking:

```bash
punch qreceive          # receiver side
punch qsend <file> <token>   # sender side
```

---

### Pipe

Stream anything to a peer without saving to disk:

```bash
# Alice streams kubectl logs to Bob
kubectl logs my-pod | punch pipe
# → prints a token, streams stdout to whoever connects

# Bob receives
punch pipe <token> > output.txt
```

---

### All commands

| Command | Description |
|---|---|
| `punch share` | Start a session, print a token, open chat |
| `punch join <token>` | Connect to a peer's token, open chat |
| `punch check` | Check if your network supports hole punching |
| `punch send <file> <token>` | Send a file directly to a peer |
| `punch receive <token>` | Receive a file from a peer |
| `punch qsend <file> <token>` | Send a file via QUIC |
| `punch qreceive` | Receive a file via QUIC |
| `punch pipe [token]` | Pipe stdin to peer, or receive a pipe to stdout |
| `punch upgrade` | Upgrade to the latest version |
| `punch version` | Show current version |

---

## How it works

### Token format

Each token encodes 8 bytes:

```
[4 bytes: public IPv4]  [2 bytes: port]  [2 bytes: session key]
```

Displayed as a short word phrase (e.g. `swift-comet — olive-TIGER-seven`) for easy dictation and copy-paste. The IP and port come from a STUN query (`stun.l.google.com`) performed on the **same UDP socket** used for the actual connection — so the NAT mapping the peer punches to is exactly the one that will receive their packets.

### NAT keepalive

While waiting for the peer's reply token, punch sends a tiny UDP keepalive to the STUN server every 20 seconds. Without this, most NATs silently expire the port mapping after 30–60 seconds of idle — meaning the address encoded in the token would be stale by the time hole punching starts.

### Hole punching

Both peers send UDP probe packets (`PUNCH`) to each other's public address simultaneously. For port-restricted cone NAT (the most common home router type), the probe from each side opens a hole in the sender's NAT for the peer's address. Once both holes are open, packets flow both ways. The process uses two STUN servers to detect symmetric NAT before attempting, so failure modes are caught early.

### Diagnostic output

Every step is visible:

```
[ ✓ ] STUN — 203.0.113.42:54321
[ ✓ ] NAT type — port-restricted, hole punching should work
[ ✓ ] verdict — your side is ready
[ ✓ ] peer address — 202.179.159.102:29344
[ ✓ ] hole punch — connected — direct P2P, no server
```

If hole punching fails, punch prints what it actually received on the socket:
- **No packets received** → your ISP is filtering incoming UDP from residential IPs. Try a mobile hotspot.
- **Packets from wrong address** → your peer's NAT is remapping ports (symmetric NAT). Same fix.

### Encryption

All traffic is encrypted with **ChaCha20-Poly1305** before leaving the machine. The key is derived from the 2-byte session embedded in the token using HKDF-SHA256. Neither side stores keys. The session is ephemeral — when both sides exit, it's gone.

```
token session bytes → HKDF-SHA256 → ChaCha20-Poly1305 key
```

The only external network call punch makes is a single UDP packet to Google's STUN server to discover your public IP. No content leaves your machine through any server.

### NAT compatibility

| NAT type | Typical location | Works? |
|---|---|---|
| Full cone | Older home routers | Yes |
| Address-restricted cone | Most home routers, cafes | Yes |
| Port-restricted cone | Corporate networks, some ISPs | Yes |
| Symmetric | Strict corporate, some mobile | No — punch tells you |
| CGNAT (double NAT) | Some ISPs (RFC 6598: 100.64.0.0/10) | No — punch tells you |

### Why "P2P" sometimes requires a relay server

UDP hole punching works when both peers can receive packets from each other's public IP:port. Some ISPs run stateful firewalls above the home router that block incoming UDP from other residential IPs regardless of NAT state — they only allow replies to connections the customer initiated to known servers.

This is why apps like Google Meet and Discord — which also use WebRTC / UDP hole punching — silently fall back to TURN relay servers when direct connection fails. TURN routes all traffic through a server, making it reliable but no longer truly peer-to-peer.

`punch` does not have a built-in relay. It succeeds as pure P2P or reports the failure clearly. If you hit the ISP-filtering case, switching to a mobile hotspot typically works because mobile carrier NAT routes differently.

---

## Security

- **End-to-end encrypted** — ChaCha20-Poly1305 with per-session keys derived via HKDF-SHA256. No server sees your plaintext.
- **No accounts, no telemetry, no analytics** — punch does not phone home.
- **Ephemeral sessions** — keys and session state live only in memory for the duration of the connection.
- **Token = shared secret** — the 2-byte session in the token is the root of trust. Share it only with the intended peer (WhatsApp/Signal DM, not a public channel).
- **No stored history** — messages and files are not written to disk by punch. Files you receive are written where you specify.

The only data that leaves your machine to any external service: one UDP packet to `stun.l.google.com` to discover your public IP. No content, no identification, just an IP echo.

---

## License

MIT
