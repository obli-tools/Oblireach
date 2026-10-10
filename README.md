# Oblireach

**Low-latency remote screen access and remote support: the remote-control component of the Obli suite.**

Oblireach captures a machine's screen, encodes it in real time and streams it to a web browser or to the Oblireach Desktop client, with keyboard and mouse control, chat, clipboard, file transfer and audio. It runs alongside [Obliance](https://github.com/obli-tools/Obliance), which deploys it and opens the sessions. A separate support mode adds ad-hoc help sessions for people who have nothing installed, through a self-hostable rendezvous server.

Website: [reach.obli.tools](https://reach.obli.tools)

## Components

| Directory | Component |
|-----------|-----------|
| `agent/` | The Oblireach agent: screen capture, encoding, input and session handling (Windows, macOS, Linux) |
| `client-app/` | Oblireach Desktop, the operator client (Windows; Linux builds) |
| `support/` | Portable support client for the person being helped (Windows, nothing to install) |
| `supportlink/` | Protocol library of the support sessions |
| `rdv/` | Self-hostable rendezvous and relay server for support sessions |
| `server/` | Standalone WebSocket relay |
| `rdphost/` | Helper that shows an RDP session as an Oblireach stream (Linux) |
| `libvpx-prebuilt/`, `svtav1-prebuilt/` | Prebuilt codec libraries used by the Windows agent |

## Key features

- **Several codecs, switchable during a session**, with adaptive bitrate: H.264 (OpenH264 or Windows Media Foundation on Windows; NVENC, VA-API or OpenH264 on Linux), VP9 and AV1 on Windows, and JPEG as a universal fallback.
- **Native capture backends**: DXGI Desktop Duplication on Windows (including the sign-in screen and UAC prompts), ScreenCaptureKit on macOS, X11 or Wayland (portal + PipeWire) on Linux.
- **Multi-monitor**: pick one screen or view all of them at once.
- **Remote input**: layout-aware keyboard and mouse, Ctrl+Alt+Del, and optional blocking of the local user's input.
- **Clipboard and files**: two-way clipboard (text and files) and file upload / download, subject to server-side permissions.
- **Audio streaming** on Windows.
- **Session following on Windows**: the stream follows sign-in, sign-out, RDP reconnection and user switching, and can lock the session when the connection ends.
- **End-user awareness**: chat with the person in front of the screen, consent prompts for remote control, on-screen indicator and notifications.
- **Oblireach Desktop**: tabbed sessions, screenshots, recording, annotation, favorites, connection import, and sign-in with Obliance or Obligate SSO.
- **Ad-hoc support sessions**: the person being helped runs the portable support client and reads an ID and a one-time code to the operator. The two sides authenticate with a password-authenticated key exchange (the server never sees the code) and compare a short verification code; traffic is end-to-end encrypted over WebRTC, peer-to-peer or relayed through TURN. The person must accept the session and can report abuse.

## Deployment

Oblireach has no standalone panel and no one-line installer. It is deployed through **Obliance**:

- the Obliance server ships the Oblireach agent, and the Obliance agent installs it on each machine and keeps it at the version the server provides;
- remote sessions are opened from the Obliance web panel (in the browser) or from Oblireach Desktop, which can be downloaded from the Obliance panel;
- access follows the Obliance permissions: a user only sees and controls the machines they are allowed to.

See [Obliance](https://github.com/obli-tools/Obliance) to install the panel.

### Self-hosting the support relay

Ad-hoc support sessions go through a rendezvous / TURN server, `rdv/`, which you can host yourself. It is built from a clone of this repository with the provided `rdv/Dockerfile`, and `rdv/docker-compose.example.yml` shows an example deployment (its header lists the steps). Configuration is done in an `.env` file based on `rdv/.env.example`: public domain name, TLS mode (built-in Let's Encrypt, certificate files, or behind a reverse proxy), operator keys or operator accounts, and optional SMTP for account verification. The server stores its data in SQLite and needs `443/tcp`, `3478/udp+tcp` and a UDP port range for relayed sessions.

## Requirements

- An [Obliance](https://github.com/obli-tools/Obliance) server for managed remote access.
- Agent: Windows (including builds for older Windows versions), macOS (Screen Recording permission required) or Linux (X11, or Wayland with the desktop portal).
- Oblireach Desktop and the agent's chat window use the Microsoft Edge WebView2 Runtime on Windows; the Windows agent installs it when it is missing.
- Support relay (optional): a Linux host with Docker and a public DNS name.

## Part of the Obli suite

| Tool | Purpose |
|------|---------|
| [Obliance](https://ance.obli.tools) | Remote monitoring and management (RMM) |
| **[Oblireach](https://reach.obli.tools)** | Remote screen access and remote support |
| [Obliview](https://view.obli.tools) | Uptime and infrastructure monitoring |
| [Oblimap](https://map.obli.tools) | IPAM and network discovery |
| [Obliguard](https://guard.obli.tools) | Agent-based intrusion prevention |
| [Obligate](https://gate.obli.tools) | Single sign-on and access management for the suite |
| [Oblihub](https://hub.obli.tools) | Docker control panel |
| [Obliwan](https://github.com/obli-tools/Obliwan) | WAN router fleet management |
| [ObliDesk](https://github.com/obli-tools/ObliDesk) | Ticketing and service desk |
| [Obliplan](https://github.com/obli-tools/Obliplan) | Workforce scheduling and working-time tracking |
| [Oblifield](https://field.obli.tools) | Field intervention management |

Suite home: [obli.tools](https://obli.tools)

## License

Oblireach is source-available under the [Elastic License 2.0](LICENSE) (ELv2). You may use, modify and self-host it, including for commercial use inside your organization; you may not provide it to others as a hosted or managed service. Third-party components (codec libraries, Microsoft WebView2 components and others) keep their own licenses, listed in [LICENSE](LICENSE).
