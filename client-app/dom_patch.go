package main

// dom_patch.go — element-wise updates of the page: the lists and the live
// sections refreshed regularly (device lists, VMs, disk usage, live gauges)
// keep their elements, and only what changed is touched. Nothing under the
// mouse, the focus, a caret, a scroll position or an open menu is rebuilt
// when its data did not change.
//
//   - domKeyed(parent, items): a keyed list (table rows...). items: [{ key,
//     sig, build(), data }] in the wanted order. A node whose key is already
//     there with the same sig (a string describing everything it shows) is
//     kept as it is (its data handed over: node._domData); a changed one is
//     built again (build() is called only then) and takes its place, the
//     focus moved to the same spot of the new node; nodes are inserted,
//     moved and removed by key. Nothing changed: nothing is touched.
//   - domMorph(el, html): an element brought to new markup in place (its
//     nodes kept, their text and attributes updated): the live sections
//     drawn from HTML strings. The same markup again: nothing is touched.
//   - domText / domAttr / domClass: set only when different.
//
// Listeners of a node kept across updates must read its current data from
// node._domData (set at each update), never from a closure over the data of
// the first build.
//
// Injected through devLiveJS (devices_live.go); no backtick here.

const domPatchJS = `
// ── Element-wise updates (dom_patch.go) ─────────────────────────────────────
// A real DOM element (the page), as opposed to a light test double.
function domIsReal(el) {
  return !!(el && el.nodeType === 1 && el.childNodes && typeof el.insertBefore === 'function' && typeof el.removeChild === 'function');
}
function domText(el, v) {
  if (!el) return false;
  v = v === null || v === undefined ? '' : String(v);
  if (el.textContent === v) return false;
  el.textContent = v;
  return true;
}
function domAttr(el, k, v) {
  if (!el || typeof el.setAttribute !== 'function') return false;
  if (v === null || v === undefined || v === false) {
    if (typeof el.hasAttribute === 'function' ? el.hasAttribute(k) : (el.getAttribute && el.getAttribute(k) != null)) { if (typeof el.removeAttribute === 'function') el.removeAttribute(k); else el.setAttribute(k, null); return true; }
    return false;
  }
  v = String(v);
  if (typeof el.getAttribute === 'function' && el.getAttribute(k) === v) return false;
  el.setAttribute(k, v);
  return true;
}
function domClass(el, cls, on) {
  if (!el || !el.classList) return false;
  on = !!on;
  if (el.classList.contains(cls) === on) return false;
  el.classList.toggle(cls, on);
  return true;
}

// ── Morph: a live node brought to a detached one ──
function domSyncAttrs(a, b) {
  var i, n;
  for (i = a.attributes.length - 1; i >= 0; i--) { n = a.attributes[i].name; if (!b.hasAttribute(n)) a.removeAttribute(n); }
  for (i = 0; i < b.attributes.length; i++) { n = b.attributes[i]; if (a.getAttribute(n.name) !== n.value) a.setAttribute(n.name, n.value); }
}
// Returns the node now in place (a, or b when a could not be kept).
function domMorphNode(a, b) {
  if (a.nodeType !== b.nodeType || a.nodeName !== b.nodeName || (a.nodeType === 1 && (a.id || '') !== (b.id || ''))) {
    a.parentNode.replaceChild(b, a);
    return b;
  }
  if (a.nodeType !== 1) { if (a.nodeValue !== b.nodeValue) a.nodeValue = b.nodeValue; return a; }
  domSyncAttrs(a, b);
  domMorphChildren(a, b);
  return a;
}
function domMorphChildren(a, b) {
  var want = Array.prototype.slice.call(b.childNodes);
  for (var i = 0; i < want.length; i++) {
    var cur = a.childNodes[i];
    if (!cur) a.appendChild(want[i]);
    else domMorphNode(cur, want[i]);
  }
  while (a.childNodes.length > want.length) a.removeChild(a.lastChild);
}
// el's content brought to html, in place. Returns true when something was
// (or may have been) written.
function domMorph(el, html) {
  if (!el) return false;
  html = html === null || html === undefined ? '' : String(html);
  var real = domIsReal(el);
  if (el._domHtml === html && (!real || (el.childNodes.length === el._domN && el.firstChild === el._domFirst))) return false;
  var tpl = real && typeof document !== 'undefined' && document && typeof document.createElement === 'function' ? document.createElement('template') : null;
  if (!tpl || !tpl.content || !tpl.content.childNodes) {
    el.innerHTML = html;
  } else {
    tpl.innerHTML = html;
    domMorphChildren(el, tpl.content);
  }
  el._domHtml = html;
  if (real) { el._domN = el.childNodes.length; el._domFirst = el.firstChild; }
  return true;
}

// ── Keyed list ──
// The path of child indexes from root down to node (null: not inside).
function domPathOf(root, node) {
  var path = [];
  for (var n = node; n && n !== root; n = n.parentNode) {
    var p = n.parentNode;
    if (!p || !p.childNodes) return null;
    path.unshift(Array.prototype.indexOf.call(p.childNodes, n));
  }
  return n === root ? path : null;
}
function domAtPath(root, path) {
  var n = root;
  for (var i = 0; n && i < path.length; i++) n = n.childNodes ? n.childNodes[path[i]] : null;
  return n || null;
}
// Stats of the last update (tests, diagnostics).
var domKeyedLast = { built: 0, kept: 0, removed: 0, moved: 0 };
function domKeyed(parent, items) {
  var st = { built: 0, kept: 0, removed: 0, moved: 0 };
  domKeyedLast = st;
  if (!parent) return st;
  parent._domHtml = null; // (a domMorph of this element before: its markup no longer holds)
  items = Array.isArray(items) ? items : [];
  var kids = Array.prototype.slice.call(parent.children || []);
  var old = new Map();
  kids.forEach(function (n) { if (n && n._domKey !== undefined && !old.has(n._domKey)) old.set(n._domKey, n); });
  var real = domIsReal(parent);
  var ae = real && typeof document !== 'undefined' ? document.activeElement : null;
  var refocus = null; // { node: the new one, path }
  var out = [];
  items.forEach(function (it) {
    var n = old.get(it.key);
    if (n) old.delete(it.key);
    if (n && n._domSig === it.sig) {
      n._domData = it.data;
      out.push(n);
      st.kept++;
      return;
    }
    var fresh = it.build();
    fresh._domKey = it.key; fresh._domSig = it.sig; fresh._domData = it.data;
    if (n && ae && typeof n.contains === 'function' && n.contains(ae)) refocus = { node: fresh, path: domPathOf(n, ae) };
    out.push(fresh);
    st.built++;
  });
  if (!real) {
    // (a test double: no node moves, the list appended again in order)
    var same = out.length === kids.length && out.every(function (n, i) { return kids[i] === n; });
    if (!same) {
      st.removed = kids.filter(function (n) { return out.indexOf(n) < 0; }).length;
      parent.innerHTML = '';
      out.forEach(function (n) { parent.appendChild(n); });
    }
    return st;
  }
  var keep = new Set(out);
  kids.forEach(function (n) { if (!keep.has(n)) { parent.removeChild(n); st.removed++; } });
  for (var i = 0; i < out.length; i++) {
    var cur = parent.children[i];
    if (cur === out[i]) continue;
    parent.insertBefore(out[i], cur || null);
    st.moved++;
  }
  if (refocus && refocus.path && document.activeElement !== ae) {
    var t = domAtPath(refocus.node, refocus.path) || refocus.node;
    try { if (t && typeof t.focus === 'function') t.focus(); } catch (e) {}
  }
  return st;
}
`
