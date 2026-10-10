package main

// webhost_rpc.go — the platform-neutral half of a UI host window
// (webhost_windows.go today, a WebKitGTK host later): the page-side binding
// glue (rpcGlue), the call of a bound Go function from a page message
// (rpcBindings), and the check that the document on screen is the local UI
// (isLocalUIURL). Moved from webhost_windows.go unchanged.

import (
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"sync"
)

// isLocalUIURL: the document is the local UI (the app's page, at "/"), not a
// page of the server (SSO) nor one of the proxy's own (/sso/callback errors).
func isLocalUIURL(raw, origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil {
		return false
	}
	if !strings.EqualFold(u.Scheme+"://"+u.Host, origin) {
		return false
	}
	return u.Path == "/" || u.Path == ""
}

// ── RPC (same protocol as go-webview2, MIT) ────────────────────────────────

type rpcBindings struct {
	mu sync.Mutex
	m  map[string]interface{}
}

func (r *rpcBindings) add(name string, f interface{}) error {
	v := reflect.ValueOf(f)
	if v.Kind() != reflect.Func {
		return errors.New("only functions can be bound")
	}
	if v.Type().NumOut() > 2 {
		return errors.New("function may only return a value or a value+error")
	}
	r.mu.Lock()
	if r.m == nil {
		r.m = map[string]interface{}{}
	}
	r.m[name] = f
	r.mu.Unlock()
	return nil
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

// call runs the binding a page message names. id is -1 for a message that is
// not a call; res is the JSON of the result.
func (r *rpcBindings) call(msg string) (id int, res string, err error) {
	var d struct {
		ID     int               `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if e := json.Unmarshal([]byte(msg), &d); e != nil {
		return -1, "", e
	}
	r.mu.Lock()
	f, ok := r.m[d.Method]
	r.mu.Unlock()
	if !ok {
		return d.ID, "null", nil
	}
	v := reflect.ValueOf(f)
	t := v.Type()
	variadic, numIn := t.IsVariadic(), t.NumIn()
	if (variadic && len(d.Params) < numIn-1) || (!variadic && len(d.Params) != numIn) {
		return d.ID, "", errors.New("function arguments mismatch")
	}
	args := make([]reflect.Value, 0, len(d.Params))
	for i := range d.Params {
		var arg reflect.Value
		if variadic && i >= numIn-1 {
			arg = reflect.New(t.In(numIn - 1).Elem())
		} else {
			arg = reflect.New(t.In(i))
		}
		if e := json.Unmarshal(d.Params[i], arg.Interface()); e != nil {
			return d.ID, "", e
		}
		args = append(args, arg.Elem())
	}
	errType := reflect.TypeOf((*error)(nil)).Elem()
	out := v.Call(args)
	var val interface{}
	switch len(out) {
	case 0:
	case 1:
		if out[0].Type().Implements(errType) {
			if e, _ := out[0].Interface().(error); e != nil {
				return d.ID, "", e
			}
		} else {
			val = out[0].Interface()
		}
	case 2:
		if !out[1].Type().Implements(errType) {
			return d.ID, "", errors.New("second return value must be an error")
		}
		if e, _ := out[1].Interface().(error); e != nil {
			return d.ID, "", e
		}
		val = out[0].Interface()
	}
	b, e := json.Marshal(val)
	if e != nil {
		return d.ID, "", e
	}
	return d.ID, string(b), nil
}

// rpcGlue: the page side of a binding (go-webview2's, unchanged).
func rpcGlue(name string) string {
	return "(function() { var name = " + jsonString(name) + ";" + `
		var RPC = window._rpc = (window._rpc || {nextSeq: 1});
		window[name] = function() {
		  var seq = RPC.nextSeq++;
		  var promise = new Promise(function(resolve, reject) {
			RPC[seq] = {
			  resolve: resolve,
			  reject: reject,
			};
		  });
		  window.external.invoke(JSON.stringify({
			id: seq,
			method: name,
			params: Array.prototype.slice.call(arguments),
		  }));
		  return promise;
		}
	})()`
}
