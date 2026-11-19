package browser

import (
	"encoding/json"
	"strconv"
	"strings"
)

// The page scripts run in the main world of the page through
// Runtime.evaluate. Element refs ("e1", "e2", ...) live in
// window.__lydiabox: a WeakMap gives an element the same ref in every
// snapshot of the same document, and a map of WeakRefs finds the element
// again. A new document starts again at e1.
//
// Each script starts with a /*lbx:<name>*/ marker (the fake CDP server of
// the tests dispatches on it) and gets its arguments as JSON literals.

const prelude = `
const S = (window.__lydiabox ||= {next: 1, byRef: new Map(), byEl: new WeakMap()});
const find = (ref) => { const w = S.byRef.get(ref); const el = w && w.deref(); return el && el.isConnected ? el : null; };
const refFor = (el) => { let r = S.byEl.get(el); if (!r) { r = 'e' + S.next++; S.byEl.set(el, r); S.byRef.set(r, new WeakRef(el)); } return r; };
const clean = (s, n) => { s = String(s ?? '').replace(/\s+/g, ' ').trim(); return s.length > n ? s.slice(0, n - 1) + '…' : s; };
const roleOf = (el) => {
  const explicit = (el.getAttribute('role') || '').trim().split(/\s+/)[0];
  if (explicit) return explicit;
  const t = el.localName;
  if (t === 'a' || t === 'area') return el.hasAttribute('href') ? 'link' : '';
  if (t === 'button' || t === 'summary') return 'button';
  if (t === 'select') return el.multiple || el.size > 1 ? 'listbox' : 'combobox';
  if (t === 'textarea') return 'textbox';
  if (t === 'input') {
    const ty = (el.getAttribute('type') || 'text').toLowerCase();
    if (ty === 'hidden') return '';
    if (['button', 'submit', 'reset', 'image', 'file', 'color'].includes(ty)) return 'button';
    if (ty === 'checkbox') return 'checkbox';
    if (ty === 'radio') return 'radio';
    if (ty === 'range') return 'slider';
    if (ty === 'number') return 'spinbutton';
    if (ty === 'search') return 'searchbox';
    return 'textbox';
  }
  if (/^h[1-6]$/.test(t)) return 'heading';
  if (el.isContentEditable && !(el.parentElement && el.parentElement.isContentEditable)) return 'textbox';
  return '';
};
const nameOf = (el) => {
  const t = el.localName;
  let n = el.getAttribute('aria-label');
  if (n && n.trim()) return n;
  const by = el.getAttribute('aria-labelledby');
  if (by) {
    n = by.split(/\s+/).map((id) => { const e = document.getElementById(id); return e ? e.innerText || e.textContent : ''; }).join(' ');
    if (n.trim()) return n;
  }
  if (el.labels && el.labels.length) {
    n = Array.from(el.labels).map((l) => l.innerText).join(' ');
    if (n.trim()) return n;
  }
  if (t === 'input') {
    const ty = (el.type || '').toLowerCase();
    if (['button', 'submit', 'reset'].includes(ty)) return el.value || ty;
    if (ty === 'image') return el.alt || el.title || 'image';
  }
  if (t !== 'input' && t !== 'textarea' && t !== 'select') {
    n = el.innerText;
    if (n && n.trim()) return n;
    const img = el.querySelector('img[alt]');
    if (img && img.alt.trim()) return img.alt;
  }
  return el.title || el.placeholder || el.getAttribute('name') || '';
};
const describe = (el) => {
  const role = roleOf(el) || el.localName;
  const name = clean(nameOf(el), 60);
  return name ? role + ' "' + name.replace(/"/g, "'") + '"' : role;
};
const screenPoint = (x, y) => {
  const side = Math.max(0, (outerWidth - innerWidth) / 2);
  const top = Math.max(0, outerHeight - innerHeight - side);
  const dpr = devicePixelRatio || 1;
  return [Math.round((screenX + side + x) * dpr), Math.round((screenY + top + y) * dpr)];
};
const centerOf = (el) => {
  let r = el.getBoundingClientRect();
  for (const c of el.getClientRects()) { if (c.width >= 1 && c.height >= 1) { r = c; break; } }
  return [r.left + r.width / 2, r.top + r.height / 2];
};
const deepHit = (x, y) => {
  let h = document.elementFromPoint(x, y);
  while (h && h.shadowRoot) { const inner = h.shadowRoot.elementFromPoint(x, y); if (!inner || inner === h) break; h = inner; }
  return h;
};
const reveal = (el) => {
  el.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
  const [x, y] = centerOf(el);
  const [sx, sy] = screenPoint(x, y);
  return {x, y, sx, sy};
};
`

const snapshotBody = `
const MAX = ARG0;
const LEAF = new Set(['link', 'button', 'textbox', 'searchbox', 'combobox', 'listbox', 'option', 'menuitem', 'menuitemcheckbox',
  'menuitemradio', 'tab', 'checkbox', 'radio', 'switch', 'slider', 'spinbutton', 'clickable']);
const ROLES = new Set([...LEAF, 'treeitem']);
const SKIP = new Set(['script', 'style', 'noscript', 'template', 'head', 'meta', 'link', 'svg', 'canvas']);
const lines = [];
let truncated = false;
const vw = innerWidth, vh = innerHeight;
const push = (line) => { if (lines.length >= MAX) { truncated = true; return; } lines.push(line); };
const pointer = (el, cs) => cs.cursor === 'pointer' && !(el.parentElement && getComputedStyle(el.parentElement).cursor === 'pointer');
const state = (el, role) => {
  const out = [];
  const t = el.localName;
  if (role === 'checkbox' || role === 'radio' || role === 'switch') {
    const c = 'checked' in el && t === 'input' ? el.checked : el.getAttribute('aria-checked') === 'true';
    out.push(c ? 'checked' : 'unchecked');
  }
  if (t === 'input' || t === 'textarea') {
    const ty = (el.type || '').toLowerCase();
    if (['checkbox', 'radio', 'button', 'submit', 'reset', 'image'].includes(ty)) { /* no value */ }
    else if (ty === 'password') { if (el.value) out.push('value=•••'); }
    else if (el.value) out.push('value="' + clean(el.value, 60).replace(/"/g, "'") + '"');
  } else if (role === 'textbox' && el.isContentEditable) {
    const v = clean(el.innerText, 60); if (v) out.push('value="' + v.replace(/"/g, "'") + '"');
  }
  if (t === 'select') {
    const sel = Array.from(el.selectedOptions).map((o) => clean(o.label || o.text, 40));
    if (sel.length) out.push('value="' + sel.join(', ') + '"');
    const opts = Array.from(el.options).map((o) => clean(o.label || o.text, 30));
    out.push('options: ' + opts.slice(0, 8).join(' | ') + (opts.length > 8 ? ' | … (' + opts.length + ')' : ''));
  }
  if (el.disabled || el.getAttribute('aria-disabled') === 'true') out.push('disabled');
  const ex = el.getAttribute('aria-expanded'); if (ex) out.push(ex === 'true' ? 'expanded' : 'collapsed');
  if (el.getAttribute('aria-selected') === 'true') out.push('selected');
  if (role === 'link' && el.href && !el.href.startsWith('javascript:')) out.push('-> ' + clean(el.href, 120));
  return out;
};
const visit = (el) => {
  if (truncated) return;
  const tag = el.localName;
  if (SKIP.has(tag) || el.getAttribute('aria-hidden') === 'true') return;
  const cs = getComputedStyle(el);
  if (cs.display === 'none') return;
  const shown = cs.visibility !== 'hidden' && cs.opacity !== '0';
  let role = roleOf(el);
  if (!role && shown && (el.hasAttribute('onclick') || pointer(el, cs)) && clean(nameOf(el), 1)) role = 'clickable';
  if (shown && role === 'heading') {
    const text = clean(el.innerText, 100);
    if (text) push('heading ' + (tag[1] || el.getAttribute('aria-level') || '') + ' "' + text.replace(/"/g, "'") + '"');
  } else if (shown && ROLES.has(role)) {
    const r = el.getBoundingClientRect();
    if (r.width >= 1 && r.height >= 1) {
      let line = '[' + refFor(el) + '] ' + role;
      const name = clean(nameOf(el), 80);
      if (name) line += ' "' + name.replace(/"/g, "'") + '"';
      const st = state(el, role);
      if (st.length) line += ' ' + st.join(' ');
      if (r.bottom < 0 || r.top > vh || r.right < 0 || r.left > vw) line += ' (offscreen)';
      push(line);
      if (LEAF.has(role)) return;
    }
  }
  if (tag === 'iframe' && shown) { push('iframe ' + clean(el.src || 'srcdoc', 100) + ' (not expanded)'); return; }
  if (el.shadowRoot) for (const c of el.shadowRoot.children) visit(c);
  for (const c of el.children) visit(c);
};
if (document.body) visit(document.body);
return {title: document.title, url: location.href, lines, truncated,
  scroll: [Math.round(scrollY), Math.round(document.documentElement.scrollHeight), vh]};
`

const locateBody = `
const el = find(ARG0);
if (!el) return {found: false};
const p = reveal(el);
const hit = deepHit(p.x, p.y);
let covered = '';
if (hit && hit !== el && !el.contains(hit) && !hit.contains(el)) covered = describe(hit);
return {found: true, desc: describe(el), covered, disabled: el.disabled === true, ...p};
`

const focusBody = `
const ref = ARG0, clear = ARG1;
const el = ref ? find(ref) : document.activeElement;
if (!el || el === document.body && ref) return {found: false};
const p = el === document.body ? {sx: 0, sy: 0} : reveal(el);
el.focus();
const t = el.localName;
const sel = getSelection();
if (t === 'input' || t === 'textarea') {
  if (clear) { el.select(); }
  else { try { const n = el.value.length; el.setSelectionRange(n, n); } catch (e) { /* type without selection API */ } }
} else if (el.isContentEditable) {
  const range = document.createRange();
  range.selectNodeContents(el);
  if (!clear) range.collapse(false);
  sel.removeAllRanges(); sel.addRange(range);
}
const active = document.activeElement;
return {found: true, desc: describe(el), password: (el.type || '') === 'password',
  focused: active === el || el.contains(active) || (el.shadowRoot && el.shadowRoot.activeElement !== null), ...p};
`

const selectBody = `
const el = find(ARG0), want = ARG1;
if (!el) return {found: false};
const p = reveal(el);
if (el.localName !== 'select') return {found: true, desc: describe(el), notSelect: true, ...p};
const opts = Array.from(el.options);
const label = (o) => clean(o.label || o.text, 200);
const pick = (v) => opts.find((o) => o.value === v) || opts.find((o) => label(o) === v) ||
  opts.find((o) => label(o).toLowerCase().includes(v.toLowerCase()));
const chosen = [], missing = [];
for (const v of want) { const o = pick(v); if (o) chosen.push(o); else missing.push(v); }
if (missing.length || (!el.multiple && chosen.length > 1)) {
  return {found: true, desc: describe(el), missing, options: opts.slice(0, 40).map(label), ...p};
}
if (el.multiple) for (const o of opts) o.selected = chosen.includes(o);
else el.value = chosen[0].value;
el.dispatchEvent(new Event('input', {bubbles: true}));
el.dispatchEvent(new Event('change', {bubbles: true}));
return {found: true, desc: describe(el), selected: Array.from(el.selectedOptions).map((o) => clean(label(o), 80)), ...p};
`

const revealBody = `
const el = find(ARG0);
if (!el) return {found: false};
return {found: true, desc: describe(el), ...reveal(el)};
`

const textBody = `
const ref = ARG0, max = ARG1;
const el = ref ? find(ref) : document.body;
if (!el) return {found: false};
const raw = (el.innerText || el.textContent || '').replace(/\n{3,}/g, '\n\n').trim();
return {found: true, text: raw.length > max ? raw.slice(0, max) : raw, total: raw.length};
`

const statusBody = `
return {title: document.title, url: location.href, ready: document.readyState, visible: document.visibilityState === 'visible'};
`

const viewportBody = `
return {w: innerWidth, h: innerHeight, y: Math.round(scrollY), x: Math.round(scrollX),
  height: Math.round(document.documentElement.scrollHeight), width: Math.round(document.documentElement.scrollWidth)};
`

const waitBody = `
const kind = ARG0, v = ARG1;
if (kind === 'load') return document.readyState === 'complete';
if (kind === 'url') return location.href.includes(v);
if (kind === 'text') return !!document.body && document.body.innerText.includes(v);
const el = document.querySelector(v);
if (!el) return false;
const r = el.getBoundingClientRect();
return r.width > 0 && r.height > 0;
`

// script builds a page script: the marker, the prelude, and body with
// ARG0, ARG1, ... replaced by the JSON encoding of args. The replacement
// is one pass, so an argument that contains "ARG1" stays as it is.
func script(name, body string, args ...any) string {
	pairs := make([]string, 0, 2*len(args))
	for i, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			b = []byte("null")
		}
		pairs = append(pairs, "ARG"+strconv.Itoa(i), string(b))
	}
	if len(pairs) > 0 {
		body = strings.NewReplacer(pairs...).Replace(body)
	}
	return "/*lbx:" + name + "*/(() => {" + prelude + body + "})()"
}
