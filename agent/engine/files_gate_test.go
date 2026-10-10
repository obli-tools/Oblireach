package engine

import (
	"encoding/json"
	"testing"
)

func TestFilesAllowedFromPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]interface{}
		want    bool
	}{
		{"older server: no field", map[string]interface{}{"sessionToken": "x"}, true},
		{"allowed", map[string]interface{}{"files": true}, true},
		{"refused", map[string]interface{}{"files": false}, false},
		{"null: fail closed", map[string]interface{}{"files": nil}, false},
		{"string: fail closed", map[string]interface{}{"files": "true"}, false},
		{"number: fail closed", map[string]interface{}{"files": float64(1)}, false},
	}
	for _, c := range cases {
		if got := FilesAllowedFromPayload(c.payload); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// Decoded from the real command JSON, like handleCommand gets it.
	var cmd struct {
		Payload map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(`{"payload":{"sessionToken":"t","files":false}}`), &cmd); err != nil {
		t.Fatal(err)
	}
	if FilesAllowedFromPayload(cmd.Payload) {
		t.Fatal("files:false from the server JSON was read as allowed")
	}
}

// helperDispatchesToXfer reproduces the capture helper's pipe reader
// (runHelperMode): its peek struct and the switch that hands a message to
// xfer.onControl. IsViewerFileMessage must catch everything it would hand over.
func helperDispatchesToXfer(payload []byte) bool {
	var peek struct {
		Type      string `json:"type"`
		Codec     string `json:"codec"`
		Bitrate   int    `json:"bitrate"`
		Index     int    `json:"index"`
		Block     bool   `json:"block"`
		Recording bool   `json:"recording"`
		Enabled   bool   `json:"enabled"`
		Mode      string `json:"mode"`
		MaxWidth  int    `json:"maxWidth"`
		MaxHeight int    `json:"maxHeight"`
		MaxPixels int    `json:"maxPixels"`
	}
	if json.Unmarshal(payload, &peek) != nil {
		return false
	}
	switch peek.Type {
	case "file_begin", "file_end", "file_cancel", "file_get", "file_progress":
		return true
	}
	return false
}

func TestViewerFileMessageDetection(t *testing.T) {
	file := []string{
		`{"type":"file_begin","tid":1,"name":"a.txt","size":3}`,
		`{"type":"file_get","tid":2,"source":"clipboard","idx":0}`,
		`{"type":"file_end","tid":1,"sha256":"00"}`,
		`{"type":"file_progress","tid":2,"seq":4}`,
		`{"type":"file_cancel","tid":2}`,
		`{"type":"file_target","bid":7,"mode":"focus"}`, // a later file_* message
		// encoding/json matches the key case-insensitively: so must the gate.
		`{"TYPE":"file_begin","tid":1,"name":"a","size":1}`,
		`{"Type":"file_get","tid":1,"idx":0}`,
		// Escaped letters decode to the same type.
		`{"type":"\u0066ile_begin","tid":1,"name":"a","size":1}`,
		// Duplicate keys: the last one wins (encoding/json), whatever the case.
		`{"type":"key","TYPE":"file_begin","tid":1,"name":"a","size":1}`,
		`{"type":"key","type":"file_get","tid":1,"idx":0}`,
	}
	for _, p := range file {
		if !IsViewerFileMessage([]byte(p)) {
			t.Errorf("file message not detected: %s", p)
		}
	}
	notFile := []string{
		`{"type":"key","action":"down","code":"KeyF"}`,
		`{"type":"mouse","action":"move","x":1,"y":2}`,
		`{"type":"clipboard_set","text":"{\"type\":\"file_begin\"}","id":"c1"}`,
		`{"type":"clipboard_sync","push":true}`,
		`{"type":"set_view","mode":"mosaic"}`,
		`{"type":"file_begin","tid":1`, // not JSON: the helper ignores it too
		`[]`,
		``,
		`{"type":1}`,
	}
	for _, p := range notFile {
		if IsViewerFileMessage([]byte(p)) {
			t.Errorf("not a file message, detected as one: %s", p)
		}
	}
	// Parity: everything the helper would hand to its transfer engine is caught.
	for _, p := range append(append([]string{}, file...), notFile...) {
		if helperDispatchesToXfer([]byte(p)) && !IsViewerFileMessage([]byte(p)) {
			t.Errorf("the helper would transfer but the gate lets it through: %s", p)
		}
	}
	// A type error on another field: the helper does not dispatch it, the gate
	// (which only decodes `type`) still refuses it — stricter is fine.
	odd := []byte(`{"type":"file_begin","tid":1,"index":"x"}`)
	if helperDispatchesToXfer(odd) || !IsViewerFileMessage(odd) {
		t.Errorf("odd message: helper=%v gate=%v", helperDispatchesToXfer(odd), IsViewerFileMessage(odd))
	}
}

func TestAgentFileMessageDetection(t *testing.T) {
	for _, p := range []string{
		`{"type":"file_accept","tid":1,"chunk":262144,"window":8,"path":"C:\\x"}`,
		`{"type":"file_offer","tid":2,"name":"a","size":3}`,
		`{"type":"file_progress","tid":1,"seq":0}`,
		`{"type":"file_done","tid":1,"ok":true}`,
		`{"type":"clipboard_files","files":[{"idx":0,"name":"a.txt","size":3}]}`,
	} {
		if !IsAgentFileMessage([]byte(p)) {
			t.Errorf("agent file message not detected: %s", p)
		}
	}
	for _, p := range []string{
		`{"type":"file_error","tid":1,"code":"forbidden"}`, // the refusal itself must pass
		`{"type":"clipboard_content","text":"my file.txt"}`,
		`{"type":"init","clipboard":{"files":false},"files":{"upload":false}}`,
		`{"type":"input_block_status","blocked":false}`,
	} {
		if IsAgentFileMessage([]byte(p)) {
			t.Errorf("wrongly detected as an agent file message: %s", p)
		}
	}
}

func TestFileRefusal(t *testing.T) {
	for _, p := range []string{
		`{"type":"file_begin","tid":12,"name":"a","size":1}`,
		`{"type":"file_get","tid":12,"source":"clipboard","idx":0}`,
		`{"type":"file_end","tid":12,"sha256":"00"}`,
		`{"TYPE":"file_begin","tid":12}`,
	} {
		b := FileRefusal([]byte(p))
		if b == nil {
			t.Fatalf("no refusal for %s", p)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if m["type"] != "file_error" || m["code"] != fxErrForbidden || m["tid"] != float64(12) {
			t.Fatalf("refusal of %s = %s", p, b)
		}
	}
	for _, p := range []string{
		`{"type":"file_progress","tid":1,"seq":0}`, // an ack: nothing to answer
		`{"type":"file_cancel","tid":1}`,
		`{"type":"file_error","tid":1,"code":"x"}`,
		`{"type":"file_begin","name":"no tid"}`,
		`{"type":"key","tid":1}`,
		`not json`,
	} {
		if b := FileRefusal([]byte(p)); b != nil {
			t.Errorf("unexpected refusal for %s: %s", p, b)
		}
	}
}

func hasFeat(list []string, f string) bool {
	for _, x := range list {
		if x == f {
			return true
		}
	}
	return false
}

func TestNoFilesFeaturesAndInit(t *testing.T) {
	defer func(v bool) { HelperNoFiles = v }(HelperNoFiles)
	fileFeats := []string{featClipboardFiles, featFileUpload, featFileDownload}

	HelperNoFiles = false
	on := agentFeatures()
	for _, f := range fileFeats {
		if !hasFeat(on, f) {
			t.Fatalf("files permitted: %q missing from %v", f, on)
		}
	}
	v := reachView{Mode: "monitor", Width: 1920, Height: 1080}
	m := buildInitMessage(1920, 1080, 30, []string{"h264"}, nil, v, 1, false, 48000, nil)
	if m["clipboard"].(map[string]interface{})["files"] != true || m["files"].(map[string]interface{})["upload"] != true ||
		m["files"].(map[string]interface{})["download"] != true {
		t.Fatalf("files permitted: init %v", m)
	}

	HelperNoFiles = true
	off := agentFeatures()
	for _, f := range fileFeats {
		if hasFeat(off, f) {
			t.Fatalf("--no-files: %q still advertised: %v", f, off)
		}
	}
	// Everything else is unchanged.
	for _, f := range on {
		if !hasFeat(fileFeats, f) && !hasFeat(off, f) {
			t.Fatalf("--no-files dropped an unrelated feature %q", f)
		}
	}
	m = buildInitMessage(1920, 1080, 30, []string{"h264"}, nil, v, 1, false, 48000, nil)
	clip := m["clipboard"].(map[string]interface{})
	files := m["files"].(map[string]interface{})
	if clip["files"] != false || clip["text"] != true || files["upload"] != false || files["download"] != false {
		t.Fatalf("--no-files: init clipboard=%v files=%v", clip, files)
	}
	for _, f := range m["features"].([]string) {
		if hasFeat(fileFeats, f) {
			t.Fatalf("--no-files: init features still list %q", f)
		}
	}
}
