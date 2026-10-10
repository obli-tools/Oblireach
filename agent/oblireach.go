//go:build ignore
// +build ignore

// ============================================================================
// agent/oblireach.go
//
// Copy this file (and oblireach_windows.go / oblireach_stub.go) into
// the Obliance agent directory to add the native Oblireach screen-streaming protocol.
//
// Build tags removed when placed in the agent package (this "ignore" tag
// prevents accidental compilation from this repo root).
// ============================================================================

package main

// Protocol overview
// -----------------
// The "oblireach" remote protocol is a lightweight screen-streaming layer
// built on top of the existing open_remote_tunnel / close_remote_tunnel
// command infrastructure already present in the Obliance agent.
//
// Instead of connecting to a local VNC or RDP service, this handler:
//   1. Connects the agent to the Oblireach relay server via WebSocket.
//   2. Starts a screen-capture goroutine that encodes frames as JPEG (v1)
//      and writes them as binary WebSocket frames.
//   3. Reads text (JSON) control frames from the relay and injects mouse /
//      keyboard events into the operating system.
//
// Frame wire format
// -----------------
//   Binary frames (agent → relay → browser):
//     [1 byte: frame type] [N bytes: payload]
//     Frame types:
//       0x01  JPEG video frame
//       0x02  Reserved (future H.264 NAL unit)
//       0x03  Reserved (future Opus audio)
//
//   Text frames (JSON, bidirectional):
//     Agent → browser:
//       {"type":"init",   "width":1920,"height":1080,"fps":15}
//       {"type":"resize", "width":2560,"height":1440}
//       {"type":"cursor", "x":960,"y":540}
//     Browser → agent:
//       {"type":"mouse",  "action":"move|down|up|scroll",
//                          "x":960,"y":540,"button":1,"delta":-3}
//       {"type":"key",    "action":"down|up","code":"KeyA",
//                          "ctrl":false,"shift":false,"alt":false,"meta":false}
//       {"type":"ping"}
//       {"type":"resize_viewport","width":1280,"height":800}
