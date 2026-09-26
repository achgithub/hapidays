// hapidays frontend. Vanilla JS, no framework/CDN — this has to keep
// working on networks that block everything but this one binary.
'use strict';

// ---------- theme ----------
// Defaults to prefers-color-scheme (see style.css); an explicit choice here
// overrides it via a data-theme attribute the CSS also checks. Applied
// immediately at parse time (the script tag is at the end of <body>, so
// documentElement already exists) rather than waiting for
// DOMContentLoaded, to avoid a flash of the wrong theme.
const THEME_KEY = 'hapidays-theme';

function getStoredTheme() {
  try { return localStorage.getItem(THEME_KEY) || ''; } catch (_) { return ''; }
}

function effectiveTheme() {
  const stored = getStoredTheme();
  if (stored === 'light' || stored === 'dark') return stored;
  return (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches) ? 'dark' : 'light';
}

function applyTheme(stored) {
  if (stored === 'light' || stored === 'dark') {
    document.documentElement.setAttribute('data-theme', stored);
  } else {
    document.documentElement.removeAttribute('data-theme');
  }
  const btn = document.getElementById('themeToggleBtn');
  if (btn) btn.title = 'Theme: ' + effectiveTheme() + ' (click to switch)';
}

applyTheme(getStoredTheme());

const state = {
  collections: [],        // summaries
  currentCollection: null, // full object incl. tree
  environments: [],
  currentEnvironmentId: '',
  selectedPath: null,      // array of indices into currentCollection.root leading to the request node
  expandedFolders: new Set(), // node ids of folders currently expanded in the tree
  settings: {},
  // Per-session TLS override ('' | 'skip' | 'enforce') — lives here rather
  // than read off a permanent sidebar control, since it's rarely touched
  // (see the field's own comment in openSettings for why it's tucked into
  // Settings instead). '' means "use the global Settings checkbox".
  tlsOverride: '',
};

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

// Quick filter over whatever's currently expanded/rendered in the tree —
// deliberately shallow (it doesn't auto-expand collapsed folders to reveal
// a match inside them). The command palette (⌘K) is the tool for "find
// this request no matter where it's collapsed/which collection it's in";
// this is just "narrow what I'm already looking at".
let treeFilterQuery = '';
function applyTreeFilter() {
  const q = treeFilterQuery.trim().toLowerCase();
  $$('#collectionList .tree-request').forEach(row => {
    row.style.display = (!q || row.textContent.toLowerCase().includes(q)) ? '' : 'none';
  });
}

async function api(path, opts) {
  const res = await fetch('/api' + path, Object.assign({ headers: { 'Content-Type': 'application/json' } }, opts));
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(body.error || res.statusText);
  }
  if (res.status === 204) return null;
  return res.json();
}

// ---------- collections / tree ----------

async function loadCollections() {
  state.collections = (await api('/collections')) || [];
  renderCollectionList();
}

function renderCollectionList() {
  saveUiState();
  const container = $('#collectionList');
  container.innerHTML = '';
  for (const col of state.collections) {
    const wrap = document.createElement('div');
    wrap.className = 'tree-node';
    const header = document.createElement('div');
    header.className = 'tree-folder';
    header.innerHTML = `<span class="row-name">${escapeHtml(col.name || '(untitled collection)')}</span>`;

    const actions = document.createElement('span');
    actions.className = 'col-actions';

    const run = document.createElement('span');
    run.className = 'col-action';
    run.textContent = '▶';
    run.title = 'Run this collection';
    run.onclick = (e) => { e.stopPropagation(); openRunModal(col.id, null, col.name); };
    actions.appendChild(run);

    const settings = document.createElement('span');
    settings.className = 'col-action';
    settings.textContent = '⚙';
    settings.title = 'Collection settings (headers, variables & auth)';
    settings.onclick = (e) => { e.stopPropagation(); openCollectionSettingsModal(col.id); };
    actions.appendChild(settings);

    const exportBtn = document.createElement('span');
    exportBtn.className = 'col-action';
    exportBtn.textContent = '⬇';
    exportBtn.title = 'Export as a hapidays collection file (re-importable via the Import button, on this machine or another)';
    exportBtn.onclick = (e) => { e.stopPropagation(); exportCollection(col.id, col.name); };
    actions.appendChild(exportBtn);

    const exportPostmanBtn = document.createElement('span');
    exportPostmanBtn.className = 'col-action';
    exportPostmanBtn.textContent = '⬇P';
    exportPostmanBtn.title = 'Export as a Postman collection file (opens directly in Postman — best-effort, see the Help panel for what doesn\'t survive the round trip)';
    exportPostmanBtn.onclick = (e) => { e.stopPropagation(); exportCollectionPostman(col.id, col.name); };
    actions.appendChild(exportPostmanBtn);

    const del = document.createElement('span');
    del.className = 'col-action col-action-danger';
    del.textContent = '✕';
    del.title = 'Delete collection';
    del.onclick = async (e) => {
      e.stopPropagation();
      if (!confirm(`Delete collection "${col.name}"?`)) return;
      await api(`/collections/${col.id}`, { method: 'DELETE' });
      if (state.currentCollection && state.currentCollection.id === col.id) state.currentCollection = null;
      await loadCollections();
    };
    actions.appendChild(del);

    header.appendChild(actions);
    // Clicking the already-open collection closes it again — otherwise the
    // only way to get back to the other collections was a page refresh.
    const isOpen = state.currentCollection && state.currentCollection.id === col.id;
    header.onclick = () => {
      if (isOpen) {
        state.currentCollection = null;
        state.selectedPath = null;
        updateCrumb();
      } else {
        openCollection(col.id);
        return; // openCollection re-renders once it has fetched the collection
      }
      renderCollectionList();
    };
    wrap.appendChild(header);
    if (isOpen) {
      const childrenWrap = document.createElement('div');
      childrenWrap.className = 'tree-children';
      childrenWrap.appendChild(buildAddRow(state.currentCollection.root, []));
      renderNodes(state.currentCollection.root, [], childrenWrap);
      wrap.appendChild(childrenWrap);
    }
    container.appendChild(wrap);
  }
  applyTreeFilter();
}

// A "+ Request" / "+ Folder" row shown at collection root and inside every
// expanded folder — the only way (besides Postman import) to build up a
// collection's tree by hand.
function buildAddRow(siblings, parentPath, folderId) {
  const row = document.createElement('div');
  row.className = 'tree-add-row';
  const addReq = document.createElement('span');
  addReq.className = 'tree-add-btn';
  addReq.textContent = '+ Request';
  addReq.onclick = (e) => { e.stopPropagation(); addRequestNode(siblings, parentPath); };
  const addFolder = document.createElement('span');
  addFolder.className = 'tree-add-btn';
  addFolder.textContent = '+ Folder';
  addFolder.onclick = (e) => { e.stopPropagation(); addFolderNode(siblings, folderId); };
  row.appendChild(addReq);
  row.appendChild(addFolder);
  return row;
}

async function addRequestNode(siblings, parentPath) {
  const name = prompt('Request name:', 'New Request');
  if (!name) return;
  const newIndex = siblings.length;
  siblings.push({ id: crypto.randomUUID(), name, request: blankRequest() });
  await persistCollectionTree();
  selectRequest(parentPath.concat(newIndex));
}

async function addFolderNode(siblings, folderId) {
  const name = prompt('Folder name:', 'New Folder');
  if (!name) return;
  const node = { id: crypto.randomUUID(), name, children: [] };
  siblings.push(node);
  if (folderId) state.expandedFolders.add(folderId); // keep the parent open so the new folder is visible
  state.expandedFolders.add(node.id); // and show the new (empty) folder expanded, not collapsed-and-invisible
  await persistCollectionTree();
}

async function renameNode(node) {
  const name = prompt('Rename to:', node.name);
  if (!name || name === node.name) return;
  node.name = name;
  await persistCollectionTree();
}

async function deleteNode(siblings, index, node) {
  const kind = node.children ? 'folder' : 'request';
  const extra = node.children && node.children.length ? ' and everything inside it' : '';
  if (!confirm(`Delete ${kind} "${node.name}"${extra}?`)) return;
  siblings.splice(index, 1);
  // Indices shift under any selection at or after this point — simplest
  // safe thing is to drop the selection rather than risk pointing at the
  // wrong node.
  state.selectedPath = null;
  currentRequest = blankRequest();
  lastSavedSnapshot = null;
  $('#dirtyDot').classList.add('hidden');
  renderRequestForm();
  updateCrumb();
  await persistCollectionTree();
}

async function persistCollectionTree() {
  state.currentCollection = await api(`/collections/${state.currentCollection.id}`, {
    method: 'PUT', body: JSON.stringify(state.currentCollection),
  });
  renderCollectionList();
}

// The auth a request set to "Inherit from collection" falls back to.
// There's no per-folder auth in this model, so the collection is the only
// thing a request can inherit from.
// Collection-level settings that otherwise had no UI at all — Variables
// (Collection.Variables) was only ever set by the Postman importer or a
// raw API call, same gap Collection Auth had before it got this modal.
async function openCollectionSettingsModal(collectionId) {
  const col = (state.currentCollection && state.currentCollection.id === collectionId)
    ? state.currentCollection
    : await api(`/collections/${collectionId}`);
  const variables = JSON.parse(JSON.stringify(col.variables || []));
  const headers = JSON.parse(JSON.stringify(col.headers || []));
  const auth = col.auth && col.auth.type ? JSON.parse(JSON.stringify(col.auth)) : { type: 'none', params: {} };
  if (!auth.params) auth.params = {};

  showModal(`
    <h3>Collection settings — ${escapeHtml(col.name)}</h3>

    <h4>Variables</h4>
    <p class="hint">Available as {{key}} to every request in this collection. For values that change per
    environment (host, port, credentials), put them in the active Environment instead — the environment's
    value wins.</p>
    <div id="colVarsTable" class="kv-table"></div>
    <button class="add-row" id="colVarsAddRow">+ Add variable</button>

    <h4>Headers</h4>
    <p class="hint">Sent on every request in this collection. A request can override one by declaring a header
    with the same name.</p>
    <div id="colHeadersTable" class="kv-table"></div>
    <button class="add-row" id="colHeadersAddRow">+ Add header</button>

    <h4>Auth</h4>
    <p class="hint">Used by any request in this collection set to "Inherit from collection".</p>
    <div class="field-row"><label>Type</label>
      <select id="colAuthType">
        <option value="none">No Auth</option>
        <option value="basic">Basic Auth</option>
        <option value="digest">Digest Auth</option>
        <option value="bearer">Bearer Token</option>
        <option value="oauth2">OAuth 2.0</option>
        <option value="apikey">API Key</option>
        <option value="awsv4">AWS Signature (SigV4)</option>
      </select>
    </div>
    <div id="colAuthFields" class="kv-table"></div>

    <div class="modal-actions">
      <button id="colSettingsCancel">Cancel</button>
      <button id="colSettingsSave" style="background:var(--accent);color:#fff">Save</button>
    </div>
  `);

  const renderVars = () => renderKVTable('colVarsTable', variables);
  renderVars();
  $('#colVarsAddRow').onclick = () => { variables.push({ key: '', value: '', disabled: false }); renderVars(); };

  const renderHeaders = () => renderKVTable('colHeadersTable', headers);
  renderHeaders();
  $('#colHeadersAddRow').onclick = () => { headers.push({ key: '', value: '', disabled: false }); renderHeaders(); };

  const rerenderAuth = () => populateAuthFields(auth.type, auth.params, $('#colAuthFields'), rerenderAuth);
  $('#colAuthType').value = auth.type;
  rerenderAuth();
  $('#colAuthType').onchange = (e) => { auth.type = e.target.value; rerenderAuth(); };

  $('#colSettingsCancel').onclick = closeModal;
  $('#colSettingsSave').onclick = async () => {
    col.variables = variables;
    col.headers = headers;
    col.auth = auth;
    const saved = await api(`/collections/${col.id}`, { method: 'PUT', body: JSON.stringify(col) });
    if (state.currentCollection && state.currentCollection.id === col.id) state.currentCollection = saved;
    closeModal();
  };
}

// ---------- remembering the UI across a browser reload ----------
//
// The selected environment, open collection, selected request and expanded
// folders are kept in localStorage so a reload (F5) puts you back where you
// were. Only ids are stored — the data itself always comes fresh from the
// server — so anything that has since been deleted is simply skipped. Storage
// can be blocked (private windows, site data cleared), so every access is
// wrapped and the app works exactly as before without it.
const UI_STATE_KEY = 'hapidays-ui-state';
// False until the restore at startup has run, so the empty state that exists
// while the page is still loading can't overwrite what was saved.
let uiStateRestored = false;

function saveUiState() {
  if (!uiStateRestored) return;
  try {
    let selectedNodeId = null;
    if (state.currentCollection && state.selectedPath) {
      try { selectedNodeId = getNodeAt(state.selectedPath).id; } catch (_) { /* stale path */ }
    }
    localStorage.setItem(UI_STATE_KEY, JSON.stringify({
      environmentId: state.currentEnvironmentId || '',
      collectionId: state.currentCollection ? state.currentCollection.id : null,
      selectedNodeId,
      expanded: Array.from(state.expandedFolders),
    }));
  } catch (_) { /* storage unavailable — state just won't persist */ }
}

function readUiState() {
  try { return JSON.parse(localStorage.getItem(UI_STATE_KEY) || 'null'); } catch (_) { return null; }
}

// Applies a previously saved state once collections and environments have
// loaded. Each piece is restored only if it still exists.
async function restoreUiState(saved) {
  if (!saved) return;
  if (saved.environmentId && state.environments.some(e => e.id === saved.environmentId)) {
    state.currentEnvironmentId = saved.environmentId;
    $('#envSelect').value = saved.environmentId;
  }
  (saved.expanded || []).forEach(id => state.expandedFolders.add(id));
  if (saved.collectionId && state.collections.some(c => c.id === saved.collectionId)) {
    try {
      state.currentCollection = await api(`/collections/${saved.collectionId}`);
    } catch (_) {
      state.currentCollection = null;
    }
    const path = (state.currentCollection && saved.selectedNodeId)
      ? findPathById(state.currentCollection.root, saved.selectedNodeId) : null;
    const node = path ? getNodeAt(path) : null;
    if (node && node.request) selectRequest(path); // also renders the tree and loads the editor
    else renderCollectionList();
  }
}

// Finds the index path to the node with this id, or null.
function findPathById(nodes, id, path = []) {
  for (let i = 0; i < nodes.length; i++) {
    const n = nodes[i];
    if (n.id === id) return path.concat(i);
    if (n.children) {
      const p = findPathById(n.children, id, path.concat(i));
      if (p) return p;
    }
  }
  return null;
}

// Re-reads collections, the open collection, environments and history from
// the server WITHOUT reloading the page, so nothing you're in the middle of
// is lost: the selected environment, the open collection, expanded folders
// and the selected request all stay put, and unsaved edits in the editor are
// left alone. Exists because a browser refresh throws all of that away, and
// data changes on disk (an import via the API, another window, a captured
// token written into an environment) otherwise can't be seen without one.
async function refreshInPlace() {
  const btn = $('#refreshBtn');
  btn.disabled = true;
  try {
    let selectedId = null;
    if (state.currentCollection && state.selectedPath) {
      try { selectedId = getNodeAt(state.selectedPath).id; } catch (_) { /* stale path — treat as nothing selected */ }
    }
    const editorDirty = lastSavedSnapshot !== null && JSON.stringify(currentRequest) !== lastSavedSnapshot;

    state.collections = (await api('/collections')) || [];
    if (state.currentCollection) {
      try {
        state.currentCollection = await api(`/collections/${state.currentCollection.id}`);
      } catch (_) {
        state.currentCollection = null; // deleted since it was opened
      }
    }
    state.selectedPath = (state.currentCollection && selectedId)
      ? findPathById(state.currentCollection.root, selectedId)
      : null;

    renderCollectionList();
    await loadEnvironments();
    await loadHistory();
    updateCrumb();

    // Show the request's latest saved state, unless there are unsaved edits —
    // those are the user's and win over whatever is on disk.
    if (state.selectedPath && !editorDirty) {
      const node = getNodeAt(state.selectedPath);
      if (node && node.request) loadRequestIntoForm(node.request);
    }
    btn.style.color = 'var(--ok)';
    setTimeout(() => { btn.style.color = ''; }, 1200);
  } catch (e) {
    alert('Refresh failed: ' + e.message);
  } finally {
    btn.disabled = false;
  }
}

async function openCollection(id) {
  state.currentCollection = await api(`/collections/${id}`);
  state.selectedPath = null;
  renderCollectionList();
}

function renderNodes(nodes, path, container) {
  nodes.forEach((node, i) => {
    const nodePath = path.concat(i);
    if (node.children) {
      const expanded = state.expandedFolders.has(node.id);
      const folderDiv = document.createElement('div');
      folderDiv.className = 'tree-folder';
      folderDiv.innerHTML = `<span class="row-name">${expanded ? '📂 ' : '📁 '}${escapeHtml(node.name)}</span>`;

      const actions = document.createElement('span');
      actions.className = 'col-actions';
      const run = document.createElement('span');
      run.className = 'col-action';
      run.textContent = '▶';
      run.title = 'Run this folder';
      run.onclick = (e) => { e.stopPropagation(); openRunModal(state.currentCollection.id, node.id, node.name); };
      actions.appendChild(run);
      const batch = document.createElement('span');
      batch.className = 'col-action';
      batch.textContent = '⛁';
      batch.title = 'Send this folder as one OData $batch request';
      batch.onclick = (e) => { e.stopPropagation(); openBatchModal(state.currentCollection.id, node.id, node.name); };
      actions.appendChild(batch);
      const rename = document.createElement('span');
      rename.className = 'col-action';
      rename.textContent = '✎';
      rename.title = 'Rename folder';
      rename.onclick = (e) => { e.stopPropagation(); renameNode(node); };
      actions.appendChild(rename);
      const del = document.createElement('span');
      del.className = 'col-action col-action-danger';
      del.textContent = '✕';
      del.title = 'Delete folder (and everything in it)';
      del.onclick = (e) => { e.stopPropagation(); deleteNode(nodes, i, node); };
      actions.appendChild(del);
      folderDiv.appendChild(actions);

      folderDiv.onclick = () => {
        if (expanded) state.expandedFolders.delete(node.id);
        else state.expandedFolders.add(node.id);
        renderCollectionList();
      };
      container.appendChild(folderDiv);

      // Collapsed by default (only nodes explicitly expanded are drawn) so
      // a collection with several folders doesn't flood the sidebar's
      // scroll region — that was the "everything else disappeared" bug.
      if (expanded) {
        const childrenWrap = document.createElement('div');
        childrenWrap.className = 'tree-children';
        childrenWrap.appendChild(buildAddRow(node.children, nodePath, node.id));
        renderNodes(node.children, nodePath, childrenWrap);
        container.appendChild(childrenWrap);
      }
    } else {
      const reqDiv = document.createElement('div');
      const method = (node.request && node.request.method) || 'GET';
      const bodyMode = node.request && node.request.body && node.request.body.mode;
      // Same protocol reasoning as currentProtocol() above: gRPC already
      // shows up via Method, but GraphQL/SOAP/OData all use an ordinary
      // HTTP method, so without this every one of them would show a plain
      // "POST" tag indistinguishable from a normal HTTP request.
      let protoClass = '', tagText = method;
      if (node.request && node.request.protocol === 'odata') { protoClass = 'proto-odata'; tagText = 'OData'; }
      else if (bodyMode === 'graphql') { protoClass = 'proto-graphql'; tagText = 'GQL'; }
      else if (bodyMode === 'soap') { protoClass = 'proto-soap'; tagText = 'SOAP'; }
      reqDiv.className = `tree-request method-${method.replace(/[^A-Za-z]/g, '')}` + (protoClass ? ' ' + protoClass : '') + (samePath(nodePath, state.selectedPath) ? ' selected' : '');
      reqDiv.innerHTML = `<span class="method-tag">${escapeHtml(tagText)}</span><span class="row-name">${escapeHtml(node.name)}</span>`;
      reqDiv.onclick = () => selectRequest(nodePath);

      const actions = document.createElement('span');
      actions.className = 'col-actions';
      const rename = document.createElement('span');
      rename.className = 'col-action';
      rename.textContent = '✎';
      rename.title = 'Rename request';
      rename.onclick = (e) => { e.stopPropagation(); renameNode(node); };
      actions.appendChild(rename);
      const del = document.createElement('span');
      del.className = 'col-action col-action-danger';
      del.textContent = '✕';
      del.title = 'Delete request';
      del.onclick = (e) => { e.stopPropagation(); deleteNode(nodes, i, node); };
      actions.appendChild(del);
      reqDiv.appendChild(actions);

      container.appendChild(reqDiv);
    }
  });
}

function samePath(a, b) {
  if (!a || !b || a.length !== b.length) return false;
  return a.every((v, i) => v === b[i]);
}

function getNodeAt(path) {
  let nodes = state.currentCollection.root;
  let node = null;
  for (const i of path) {
    node = nodes[i];
    nodes = node.children;
  }
  return node;
}

function selectRequest(path) {
  state.selectedPath = path;
  renderCollectionList();
  const node = getNodeAt(path);
  loadRequestIntoForm(node.request);
}

// ---------- request form ----------

let currentRequest = blankRequest();
// Snapshot of the last-loaded-or-saved request, used only to drive the
// unsaved-changes dot in the crumb bar — compared against a fresh
// collectFormIntoRequest() read on every edit. Not involved in Send/Save.
let lastSavedSnapshot = null;

function blankRequest() {
  return {
    method: 'GET', urlRaw: '', query: [], headers: [],
    auth: { type: 'none', params: {} },
    body: { mode: 'none' },
    protocol: '',
    captures: [], assertions: [], preRequestScript: '', testScript: '', hasScript: false,
  };
}

function loadRequestIntoForm(req) {
  currentRequest = JSON.parse(JSON.stringify(req));
  if (!currentRequest.auth) currentRequest.auth = { type: 'none', params: {} };
  if (!currentRequest.auth.params) currentRequest.auth.params = {};
  if (!currentRequest.body) currentRequest.body = { mode: 'none' };
  renderRequestForm();
  // The form now mirrors currentRequest exactly (composeUrlFromFields etc.
  // just normalized it) — that's the "saved" baseline for the dirty dot.
  collectFormIntoRequest();
  lastSavedSnapshot = JSON.stringify(currentRequest);
  $('#dirtyDot').classList.add('hidden');
  updateCrumb();
}

// ---------- URL bar: Protocol / Domain / Port / Path fields ----------
//
// Rather than editing "https://host:port/path?query" as one string (and
// having a port field try to live-parse-and-rewrite that string, which
// turned out fragile — e.g. it silently did nothing when the host was a
// bare {{var}} token, since a regex trying to spot "http(s)://" found
// nothing to anchor on), the URL bar is four independent fields that
// only ever compose FORWARD into urlRaw. Decomposition only happens once,
// when a request is loaded — not continuously on every keystroke — which
// is what actually makes this robust: a one-shot best-effort parse with a
// visible "did this round-trip correctly?" preview underneath, rather
// than a live parser that has to get every edge case right on every
// keypress or silently misbehave.
//
// Path never contains "?..." — the Query Params tab is the sole source
// of query params (see applyQueryParams in internal/client/execute.go);
// any literal query string found in a loaded request's URL gets folded
// into that tab once, on load, then dropped from the composed URL.
const URL_DECOMPOSE_RE = /^(?:(https?):\/\/)?([^/:?#]+)?(?::(\d+))?([^?#]*)(?:\?([^#]*))?/;

// True when the domain is a single {{var}} token.
function domainIsVariable(domain) {
  return domain.trim().startsWith('{{');
}

// Set when the loaded request wrote its scheme out explicitly in front of a
// variable host ("https://{{host}}/path" — what Postman collections
// typically look like). In that case the scheme is the user's, so it is kept
// and Protocol stays editable. A bare "{{baseUrl}}" with no scheme in front
// is still treated as carrying its own (e.g. baseUrl = "https://api.x.com"),
// so Protocol is disabled rather than double-prepended.
let keepSchemeOnVarDomain = false;

function protocolIgnored(domain) {
  return domainIsVariable(domain) && !keepSchemeOnVarDomain;
}

function updateProtocolFieldState() {
  $('#protocolSelect').disabled = protocolIgnored($('#domainInput').value);
}

// Rebuilds currentRequest.urlRaw from the four fields (never includes a
// query string — that's the Query Params tab's job) and refreshes the
// human-facing preview, which DOES include Query Params, so it shows
// what will truly be sent, not just what's in urlRaw.
function composeUrlFromFields() {
  const domain = $('#domainInput').value.trim();
  const port = $('#portInput').value.trim();
  const path = $('#pathInput').value;
  updateProtocolFieldState();

  let url = protocolIgnored(domain) ? domain : (domain ? `${$('#protocolSelect').value}://${domain}` : '');
  if (port) url += `:${port}`;
  url += path;
  currentRequest.urlRaw = url;
  refreshFullUrlPreview();
}

function refreshFullUrlPreview() {
  let preview = currentRequest.urlRaw || '';
  const enabledQuery = (currentRequest.query || []).filter(kv => !kv.disabled && kv.key);
  if (enabledQuery.length) {
    const qs = enabledQuery.map(kv => `${encodeURIComponent(kv.key)}=${encodeURIComponent(kv.value)}`).join('&');
    preview += (preview.includes('?') ? '&' : '?') + qs;
  }
  $('#fullUrlPreview').textContent = preview || '(empty URL)';
}

// Splits a loaded request's urlRaw into the four fields, folding any
// literal query string into the Query Params list (dedup'd by key+value,
// so re-loading the same request repeatedly can't pile up duplicates).
function loadUrlFieldsFromRequest() {
  const raw = currentRequest.urlRaw || '';
  const m = raw.match(URL_DECOMPOSE_RE) || [];
  const [, protocol, domain, port, path, query] = m;

  if (query) {
    const existing = currentRequest.query || (currentRequest.query = []);
    for (const [key, value] of new URLSearchParams(query).entries()) {
      if (!existing.some(kv => kv.key === key && kv.value === value)) {
        existing.push({ key, value, disabled: false });
      }
    }
  }

  $('#protocolSelect').value = protocol || 'https';
  keepSchemeOnVarDomain = !!protocol && (domain || '').trim().startsWith('{{');
  $('#domainInput').value = domain || '';
  $('#portInput').value = port || '';
  $('#pathInput').value = path || '';
  composeUrlFromFields(); // also strips any leftover literal "?..." from urlRaw now that it's in Query Params
}

function renderRequestForm() {
  $('#methodSelect').value = currentRequest.method || 'GET';
  loadUrlFieldsFromRequest();
  renderKVTable('paramsTable', currentRequest.query || (currentRequest.query = []), refreshFullUrlPreview);
  renderKVTable('headersTable', currentRequest.headers || (currentRequest.headers = []));
  renderCapturesTable();
  renderAssertionsTable();

  $('#authType').value = currentRequest.auth.type || 'none';
  renderAuthFields();

  $('#bodyMode').value = currentRequest.body.mode || 'none';
  $('#bodyLanguage').value = currentRequest.body.rawLanguage || 'json';
  if (currentRequest.body.mode === 'grpc') {
    $('#bodyRaw').value = (currentRequest.body.grpc && currentRequest.body.grpc.requestJson) || '';
  } else if (currentRequest.body.mode === 'graphql') {
    const [query, variables] = splitGraphqlBody(currentRequest.body);
    $('#bodyRaw').value = query;
    $('#graphqlVariables').value = variables;
  } else {
    $('#bodyRaw').value = currentRequest.body.raw || '';
  }
  $('#soapVersion').value = currentRequest.body.soapVersion || '1.1';
  $('#soapAction').value = currentRequest.body.soapAction || '';
  $('#wsSecurityMode').value = currentRequest.body.wsSecurityMode || '';
  $('#wsSecurityUsername').value = currentRequest.body.wsSecurityUsername || '';
  $('#wsSecurityPassword').value = currentRequest.body.wsSecurityPassword || '';
  $('#signBody').checked = !!currentRequest.body.signBody;
  renderBodyFields();

  $('#preScriptView').value = currentRequest.preRequestScript || '';
  $('#testScriptView').value = currentRequest.testScript || '';

  updateProtoSwitchUI();
  updateTabBadges();
}

// ---------- protocol switcher ----------
//
// A request's "protocol" isn't a stored field — it's implied by method +
// body.mode (see internal/model: BodyMode is none/raw/urlencoded/formdata/
// graphql/soap/grpc, and gRPC additionally sets Method to "GRPC"). This
// switcher is a friendlier front end for that same state: picking GraphQL/
// SOAP/gRPC sets body.mode (and method, for gRPC) exactly like the old
// Body-tab dropdown did, so nothing downstream (renderBodyFields, send,
// save) needed to change. HTTP covers the four body-shape modes that
// dropdown still owns (none/raw/urlencoded/formdata).
function currentProtocol() {
  if (currentRequest.method === 'GRPC' || currentRequest.body.mode === 'grpc') return 'grpc';
  if (currentRequest.body.mode === 'graphql') return 'graphql';
  if (currentRequest.body.mode === 'soap') return 'soap';
  // OData isn't a body shape (it's plain HTTP — see model.RequestSpec.Protocol's
  // doc comment) so it only wins when none of the above already claimed the
  // request; it's tracked via the request's own protocol field, set by the
  // OData importer or by picking this chip by hand.
  if (currentRequest.protocol === 'odata') return 'odata';
  return 'http';
}

function updateProtoSwitchUI() {
  const proto = currentProtocol();
  $$('#protoSwitch .proto-opt').forEach(btn => {
    const active = btn.dataset.p === proto;
    btn.classList.toggle('active', active);
    btn.setAttribute('aria-selected', String(active));
  });
}

function setProtocol(p) {
  if (p === currentProtocol()) return;
  currentRequest.protocol = ''; // cleared by default; only 'odata' below sets it back
  if (p === 'http') {
    if (['graphql', 'soap', 'grpc'].includes(currentRequest.body.mode)) currentRequest.body.mode = 'none';
    if (currentRequest.method === 'GRPC') currentRequest.method = 'GET';
  } else if (p === 'graphql') {
    currentRequest.body.mode = 'graphql';
    if (currentRequest.method === 'GRPC') currentRequest.method = 'POST';
  } else if (p === 'soap') {
    currentRequest.body.mode = 'soap';
    if (currentRequest.method === 'GRPC') currentRequest.method = 'POST';
  } else if (p === 'grpc') {
    currentRequest.body.mode = 'grpc';
    currentRequest.method = 'GRPC';
  } else if (p === 'odata') {
    // Plain HTTP on the wire — OData is just a tag, so drop any leftover
    // GraphQL/SOAP/gRPC body shape rather than layer the tag on top of one.
    if (['graphql', 'soap', 'grpc'].includes(currentRequest.body.mode)) currentRequest.body.mode = 'none';
    if (currentRequest.method === 'GRPC') currentRequest.method = 'GET';
    currentRequest.protocol = 'odata';
  }
  $('#methodSelect').value = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'].includes(currentRequest.method) ? currentRequest.method : 'GET';
  $('#bodyMode').value = currentRequest.body.mode;
  renderBodyFields();
  updateProtoSwitchUI();
}

// ---------- tab badges & crumb ----------

function setTabBadge(tabName, count) {
  const tab = document.querySelector(`.tab[data-tab="${tabName}"]`);
  if (!tab) return;
  let badge = tab.querySelector('.tab-count');
  if (count > 0) {
    if (!badge) { badge = document.createElement('span'); badge.className = 'tab-count'; tab.appendChild(badge); }
    badge.textContent = String(count);
  } else if (badge) {
    badge.remove();
  }
}

function updateTabBadges() {
  setTabBadge('params', (currentRequest.query || []).length);
  setTabBadge('headers', (currentRequest.headers || []).length);
  setTabBadge('script', (currentRequest.captures || []).length);
  setTabBadge('assertions', (currentRequest.assertions || []).length);
  const authTab = document.querySelector('.tab[data-tab="auth"]');
  if (authTab) authTab.classList.toggle('auth-active', !!currentRequest.auth.type && currentRequest.auth.type !== 'none');
  updateAuthBanner();
}

const AUTH_LABELS = {
  basic: 'Basic Auth', digest: 'Digest Auth', bearer: 'Bearer Token',
  oauth2: 'OAuth 2.0', apikey: 'API Key', awsv4: 'AWS Signature (SigV4)',
};

// Surfaces what's actually going to authenticate this request without
// having to click into the Auth tab — "inherit" in particular is opaque
// otherwise, since the request itself carries no clue what it inherits.
function updateAuthBanner() {
  const banner = $('#authBanner');
  const type = currentRequest.auth.type;
  if (!type || type === 'none') {
    banner.classList.add('hidden');
    banner.innerHTML = '';
    return;
  }
  const lockIcon = '<svg class="lock" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/></svg>';
  let text;
  if (type === 'inherit') {
    const colAuth = state.currentCollection && state.currentCollection.auth;
    if (colAuth && colAuth.type && colAuth.type !== 'none') {
      text = `Inherits <b>${AUTH_LABELS[colAuth.type] || colAuth.type}</b> from "${escapeHtml(state.currentCollection.name)}" — switch to the Auth tab to override.`;
    } else if (state.currentCollection) {
      text = `Set to inherit from "${escapeHtml(state.currentCollection.name)}", but that collection has no auth configured — this request sends with none.`;
    } else {
      text = `Set to inherit from the collection — open the collection this request belongs to for the actual auth used.`;
    }
  } else {
    text = `Uses <b>${AUTH_LABELS[type] || type}</b> for this request only, overriding whatever the collection is set to.`;
  }
  banner.classList.remove('hidden');
  banner.innerHTML = lockIcon + ' ' + text;
}

// Best-effort path through the tree to the selected request, purely for
// the crumb bar — falls back to a method+URL label for history entries,
// which aren't tied to a tree node (state.selectedPath is null for those).
function updateCrumb() {
  const el = $('#crumbText');
  if (state.currentCollection && state.selectedPath) {
    const names = [state.currentCollection.name];
    let nodes = state.currentCollection.root;
    for (const i of state.selectedPath) {
      const node = nodes[i];
      if (!node) break;
      names.push(node.name);
      nodes = node.children;
    }
    const current = names.pop();
    const prefix = names.map(n => escapeHtml(n)).join(' <span class="sep">/</span> ');
    el.innerHTML = (prefix ? prefix + ' <span class="sep">/</span> ' : '') + `<span class="current">${escapeHtml(current)}</span>`;
  } else if (currentRequest && (currentRequest.urlRaw || currentRequest.body.mode === 'grpc')) {
    el.textContent = `(unsaved) ${currentRequest.method} ${currentRequest.urlRaw || (currentRequest.body.grpc && currentRequest.body.grpc.fullMethod) || ''}`;
  } else {
    el.textContent = 'No request selected';
  }
}

// Recomputes currentRequest from the DOM and refreshes the dirty dot + tab
// badges. Wired once as a delegated listener on #requestWorkspace rather
// than per-field, so it also covers rows added/removed after the initial
// render (kv-tables rebuild their DOM on every change).
function onWorkspaceChanged() {
  collectFormIntoRequest();
  const dirty = lastSavedSnapshot !== null && JSON.stringify(currentRequest) !== lastSavedSnapshot;
  $('#dirtyDot').classList.toggle('hidden', !dirty);
  updateTabBadges();
}

function renderKVTable(containerId, list, onChange) {
  const container = $('#' + containerId);
  container.innerHTML = '';
  list.forEach((kv, i) => {
    const row = document.createElement('div');
    row.className = 'kv-row';
    row.innerHTML = `
      <input type="checkbox" ${kv.disabled ? '' : 'checked'} title="Enabled">
      <input type="text" placeholder="Key" value="${escapeAttr(kv.key)}">
      <input type="text" placeholder="Value" value="${escapeAttr(kv.value)}">
      <button class="remove-row" title="Remove">×</button>`;
    const [chk, keyInput, valInput] = row.querySelectorAll('input');
    chk.onchange = () => { kv.disabled = !chk.checked; if (onChange) onChange(); };
    keyInput.oninput = () => { kv.key = keyInput.value; if (onChange) onChange(); };
    valInput.oninput = () => { kv.value = valInput.value; if (onChange) onChange(); };
    row.querySelector('.remove-row').onclick = () => { list.splice(i, 1); renderKVTable(containerId, list, onChange); if (onChange) onChange(); };
    container.appendChild(row);
  });
}

function renderCapturesTable() {
  const container = $('#capturesTable');
  const list = currentRequest.captures || (currentRequest.captures = []);
  container.innerHTML = '';
  list.forEach((cap, i) => {
    const row = document.createElement('div');
    row.className = 'kv-row';
    row.innerHTML = `
      <select style="flex:0 0 90px">
        <option value="header" ${cap.source === 'header' ? 'selected' : ''}>Header</option>
        <option value="body_json" ${cap.source === 'body_json' ? 'selected' : ''}>JSON body</option>
      </select>
      <input type="text" placeholder="From (header name or a.b.c path)" value="${escapeAttr(cap.from)}">
      <input type="text" placeholder="Into var" value="${escapeAttr(cap.intoVar)}">
      <button class="remove-row" title="Remove">×</button>`;
    const sel = row.querySelector('select');
    const [fromInput, intoInput] = row.querySelectorAll('input[type=text]');
    sel.onchange = () => { cap.source = sel.value; };
    fromInput.oninput = () => { cap.from = fromInput.value; };
    intoInput.oninput = () => { cap.intoVar = intoInput.value; };
    row.querySelector('.remove-row').onclick = () => { list.splice(i, 1); renderCapturesTable(); };
    container.appendChild(row);
  });
}

const ASSERTION_TYPES = [
  { value: 'status_equals', label: 'Status equals', needsTarget: false, needsExpected: true, expectedPlaceholder: '200' },
  { value: 'status_range', label: 'Status in range', needsTarget: false, needsExpected: true, expectedPlaceholder: '2xx' },
  { value: 'header_exists', label: 'Header exists', needsTarget: true, needsExpected: false, targetPlaceholder: 'Header name' },
  { value: 'header_equals', label: 'Header equals', needsTarget: true, needsExpected: true, targetPlaceholder: 'Header name', expectedPlaceholder: 'Value' },
  { value: 'body_contains', label: 'Body contains', needsTarget: false, needsExpected: true, expectedPlaceholder: 'substring' },
  { value: 'json_path_exists', label: 'JSON path exists', needsTarget: true, needsExpected: false, targetPlaceholder: 'data.token' },
  { value: 'json_path_equals', label: 'JSON path equals', needsTarget: true, needsExpected: true, targetPlaceholder: 'data.token', expectedPlaceholder: 'Value' },
  { value: 'max_duration_ms', label: 'Max duration (ms)', needsTarget: false, needsExpected: true, expectedPlaceholder: '2000' },
];

function renderAssertionsTable() {
  const container = $('#assertionsTable');
  const list = currentRequest.assertions || (currentRequest.assertions = []);
  container.innerHTML = '';
  list.forEach((a, i) => {
    const meta = ASSERTION_TYPES.find(t => t.value === a.type) || ASSERTION_TYPES[0];
    const row = document.createElement('div');
    row.className = 'kv-row';
    row.innerHTML = `
      <input type="checkbox" ${a.disabled ? '' : 'checked'} title="Enabled">
      <select style="flex:0 0 150px">
        ${ASSERTION_TYPES.map(t => `<option value="${t.value}" ${t.value === a.type ? 'selected' : ''}>${t.label}</option>`).join('')}
      </select>
      <input type="text" class="assert-target" placeholder="${meta.targetPlaceholder || ''}" value="${escapeAttr(a.target || '')}" ${meta.needsTarget ? '' : 'disabled'}>
      <input type="text" class="assert-expected" placeholder="${meta.expectedPlaceholder || ''}" value="${escapeAttr(a.expected || '')}" ${meta.needsExpected ? '' : 'disabled'}>
      <button class="remove-row" title="Remove">×</button>`;
    const chk = row.querySelector('input[type=checkbox]');
    const sel = row.querySelector('select');
    const targetInput = row.querySelector('.assert-target');
    const expectedInput = row.querySelector('.assert-expected');
    chk.onchange = () => { a.disabled = !chk.checked; };
    sel.onchange = () => {
      a.type = sel.value;
      const newMeta = ASSERTION_TYPES.find(t => t.value === a.type);
      if (!newMeta.needsTarget) a.target = '';
      if (!newMeta.needsExpected) a.expected = '';
      renderAssertionsTable();
    };
    targetInput.oninput = () => { a.target = targetInput.value; };
    expectedInput.oninput = () => { a.expected = expectedInput.value; };
    row.querySelector('.remove-row').onclick = () => { list.splice(i, 1); renderAssertionsTable(); };
    container.appendChild(row);
  });
}

function renderAssertionSummary(assertions) {
  const el = $('#assertionSummary');
  if (!assertions || assertions.length === 0) {
    el.classList.add('hidden');
    el.innerHTML = '';
    return;
  }
  // Headline pass/fail count lives in the #respAssertPill next to the
  // status now (see setAssertPill) — this box is just the per-assertion
  // detail underneath it, so the count isn't stated twice.
  const allPassed = assertions.every(a => a.passed);
  el.classList.remove('hidden');
  el.className = allPassed ? 'assertion-summary assertion-pass' : 'assertion-summary assertion-fail';
  el.innerHTML = '<ul>' + assertions.map(a =>
      `<li class="${a.passed ? 'assertion-pass' : 'assertion-fail'}">${a.passed ? '✓' : '✗'} ${escapeHtml(a.message || a.type)}</li>`
    ).join('') + '</ul>';
}

// Best-effort scan of the request's imported Postman scripts for the idioms
// that map cleanly onto a Capture rule or an Assertion (status checks,
// storing a response header/JSON field in a variable). The recognition
// itself lives on the server (internal/importer/scripts.go) so the button
// and the importer can never disagree. Anything with conditionals, loops, or
// string manipulation isn't recognized and still needs a human to read it.
async function runSuggestCaptures() {
  const script = (currentRequest.testScript || '') + '\n' + (currentRequest.preRequestScript || '');
  let found;
  try {
    found = await api('/scripts/suggest', { method: 'POST', body: JSON.stringify({ script }) });
  } catch (err) {
    alert('Could not scan the script: ' + err.message);
    return;
  }
  const captures = currentRequest.captures || (currentRequest.captures = []);
  const assertions = currentRequest.assertions || (currentRequest.assertions = []);
  let added = 0;
  for (const c of found.captures) {
    if (!captures.some(e => e.source === c.source && e.from === c.from && e.intoVar === c.intoVar)) { captures.push(c); added++; }
  }
  for (const a of found.assertions) {
    if (!assertions.some(e => e.type === a.type && (e.target || '') === (a.target || '') && (e.expected || '') === (a.expected || ''))) { assertions.push(a); added++; }
  }
  renderCapturesTable();
  renderAssertionsTable();
  updateTabBadges();
  if (found.captures.length + found.assertions.length === 0) {
    alert('No recognizable capture or status-check patterns found in the scripts above — this script likely needs manual conversion (conditionals, loops, or chained requests aren\'t supported by Capture or Assertion rules).');
  } else if (added === 0) {
    alert('Found the same pattern(s) already present as rules — nothing new to add.');
  }
}

function renderAuthFields() {
  populateAuthFields(currentRequest.auth.type, currentRequest.auth.params, $('#authFields'), renderAuthFields);
}

// Builds the fields for one auth type into container. Generic over which
// auth object it's editing (a request's or a collection's) — rerender is
// called back when a sub-control (like oauth2's grant type) needs the
// field set redrawn.
function populateAuthFields(type, params, container, rerender) {
  container.innerHTML = '';
  const field = (key, placeholder, isPassword) => {
    const input = document.createElement('input');
    input.type = isPassword ? 'password' : 'text';
    input.placeholder = placeholder;
    input.value = params[key] || '';
    input.oninput = () => { params[key] = input.value; };
    container.appendChild(input);
  };
  if (type === 'basic' || type === 'digest') {
    field('username', 'Username');
    field('password', 'Password', true);
  } else if (type === 'bearer') {
    field('token', 'Token');
  } else if (type === 'apikey') {
    field('key', 'Key name (e.g. X-API-Key)');
    field('value', 'Value');
    const addTo = document.createElement('select');
    addTo.innerHTML = '<option value="header">Header</option><option value="query">Query param</option>';
    addTo.value = params.in || 'header';
    addTo.onchange = () => { params.in = addTo.value; };
    container.appendChild(addTo);
  } else if (type === 'awsv4') {
    field('accessKey', 'Access Key ID');
    field('secretKey', 'Secret Access Key', true);
    field('sessionToken', 'Session Token (optional, for temporary credentials)');
    field('region', 'Region (e.g. us-east-1)');
    field('service', 'Service (e.g. execute-api, s3)');
  } else if (type === 'oauth2') {
    renderOAuth2Fields(container, params, rerender);
  }
}

function renderOAuth2Fields(container, params, rerender) {
  const field = (key, placeholder, isPassword) => {
    const input = document.createElement('input');
    input.type = isPassword ? 'password' : 'text';
    input.placeholder = placeholder;
    input.value = params[key] || '';
    input.oninput = () => { params[key] = input.value; };
    container.appendChild(input);
  };
  const grantSelect = document.createElement('select');
  grantSelect.innerHTML = `
    <option value="client_credentials">Client Credentials</option>
    <option value="password">Username &amp; Password</option>
    <option value="authorization_code">Authorization Code</option>`;
  grantSelect.value = params.grantType || 'client_credentials';
  grantSelect.onchange = () => { params.grantType = grantSelect.value; rerender(); };
  container.appendChild(grantSelect);

  field('accessTokenUrl', 'Access Token URL');
  if (grantSelect.value === 'authorization_code') field('authUrl', 'Authorization URL');
  field('clientId', 'Client ID');
  field('clientSecret', 'Client Secret', true);
  if (grantSelect.value === 'password') {
    field('username', 'Username');
    field('password', 'Password', true);
  }
  field('scope', 'Scope (optional)');

  const tokenRow = document.createElement('div');
  tokenRow.className = 'kv-row';
  const tokenPreview = document.createElement('input');
  tokenPreview.type = 'text';
  tokenPreview.placeholder = 'No token fetched yet';
  tokenPreview.value = params.accessToken || '';
  tokenPreview.readOnly = true;
  const getTokenBtn = document.createElement('button');
  getTokenBtn.textContent = 'Get New Access Token';
  getTokenBtn.onclick = async () => {
    getTokenBtn.disabled = true;
    getTokenBtn.textContent = grantSelect.value === 'authorization_code' ? 'Waiting on browser…' : 'Fetching…';
    try {
      const result = await fetchOAuth2Token(params, grantSelect.value);
      params.accessToken = result.accessToken;
      if (result.refreshToken) params.refreshToken = result.refreshToken;
      tokenPreview.value = params.accessToken;
      refreshTokenBtn.hidden = !params.refreshToken;
    } catch (e) {
      alert('OAuth2 token fetch failed: ' + e.message);
    } finally {
      getTokenBtn.disabled = false;
      getTokenBtn.textContent = 'Get New Access Token';
    }
  };
  // Only shown once a grant has actually returned a refresh_token — not
  // every server issues one, and it means re-running the whole
  // authorize/token dance isn't the only way to get a live token again.
  const refreshTokenBtn = document.createElement('button');
  refreshTokenBtn.textContent = 'Refresh Token';
  refreshTokenBtn.hidden = !params.refreshToken;
  refreshTokenBtn.onclick = async () => {
    refreshTokenBtn.disabled = true;
    try {
      const result = await api('/oauth2/token', {
        method: 'POST',
        body: JSON.stringify({
          grantType: 'refresh_token', accessTokenUrl: params.accessTokenUrl,
          clientId: params.clientId, clientSecret: params.clientSecret,
          refreshToken: params.refreshToken, scope: params.scope,
        }),
      });
      params.accessToken = result.accessToken;
      if (result.refreshToken) params.refreshToken = result.refreshToken; // servers commonly rotate it
      tokenPreview.value = params.accessToken;
    } catch (e) {
      alert('OAuth2 token refresh failed: ' + e.message);
    } finally {
      refreshTokenBtn.disabled = false;
    }
  };
  tokenRow.appendChild(tokenPreview);
  tokenRow.appendChild(getTokenBtn);
  tokenRow.appendChild(refreshTokenBtn);
  container.appendChild(tokenRow);
}

async function fetchOAuth2Token(params, grantType) {
  if (grantType === 'authorization_code') {
    const start = await api('/oauth2/authorize/start', {
      method: 'POST',
      body: JSON.stringify({
        authUrl: params.authUrl, accessTokenUrl: params.accessTokenUrl,
        clientId: params.clientId, clientSecret: params.clientSecret, scope: params.scope,
      }),
    });
    window.open(start.authUrl, '_blank');
    return await api('/oauth2/authorize/wait', { method: 'POST', body: JSON.stringify({ sessionId: start.sessionId }) });
  }
  return await api('/oauth2/token', {
    method: 'POST',
    body: JSON.stringify({
      grantType, accessTokenUrl: params.accessTokenUrl,
      clientId: params.clientId, clientSecret: params.clientSecret,
      username: params.username, password: params.password, scope: params.scope,
    }),
  });
}

// SOAP has its own "Format XML" button (always XML, tied to well-formedness
// checking); grpc forces JSON but hides the language picker entirely;
// GraphQL's textarea holds a query, not JSON/XML, so there's nothing here
// to reformat. This button only applies to plain Raw, where the language
// picker is live.
function updatePrettifyVisibility() {
  const mode = currentRequest.body.mode;
  const show = mode === 'raw' && $('#bodyLanguage').value !== 'text';
  $('#bodyPrettify').classList.toggle('hidden', !show);
}

// Raw (JSON/XML — Text has nothing worth tokenizing) and SOAP (always
// XML) both hold something worth coloring; GraphQL's textarea holds a
// query and gRPC's is protojson that's fine plain, so neither applies
// here. See the CSS comment on .code-editor for why this only touches
// text/background/caret-color on #bodyRaw rather than switching it to
// contenteditable.
function updateBodyRawHighlight() {
  const pre = $('#bodyRawHighlight');
  const textarea = $('#bodyRaw');
  const mode = currentRequest.body.mode;
  const lang = mode === 'soap' ? 'xml' : $('#bodyLanguage').value;
  const show = (mode === 'raw' && (lang === 'json' || lang === 'xml')) || mode === 'soap';
  pre.classList.toggle('hidden', !show);
  textarea.classList.toggle('code-editor-input-active', show);
  if (!show) return;
  try {
    const html = lang === 'json' ? highlightJson(textarea.value) : highlightXml(textarea.value);
    // A trailing newline gets no final line box in the <pre>, shorting its
    // scrollHeight by one line versus the textarea and clamping the scroll
    // sync below the true bottom — pad it back in.
    pre.innerHTML = textarea.value.endsWith('\n') ? html + '\n' : html;
  } catch (_) {
    pre.textContent = textarea.value; // tokenizer choked on something mid-edit — show it uncolored rather than stale
  }
  pre.scrollTop = textarea.scrollTop;
  pre.scrollLeft = textarea.scrollLeft;
}

// Collections saved before the split query/variables editor existed stored
// the already-assembled {"query","variables"} JSON directly in body.raw
// (see buildBody's back-compat path in execute.go) — split it once here so
// opening an old request shows it in the new fields instead of empty ones.
// Returns [query, variablesText].
function splitGraphqlBody(body) {
  if (body.graphqlQuery) return [body.graphqlQuery, body.graphqlVariables || ''];
  if (body.raw) {
    try {
      const parsed = JSON.parse(body.raw);
      if (typeof parsed.query === 'string') {
        return [parsed.query, parsed.variables !== undefined ? JSON.stringify(parsed.variables, null, 2) : ''];
      }
    } catch (_) { /* not the assembled JSON shape — fall through */ }
    return [body.raw, ''];
  }
  return ['', ''];
}

function renderBodyFields() {
  const mode = currentRequest.body.mode;
  // The Query editor reuses the raw-body textarea (like gRPC reuses it for
  // its protojson editor) since it's the same "one big text box" shape;
  // Variables gets its own box in #graphqlFields since it's a second field.
  // soap also reuses the textarea (it's just the envelope XML) but swaps
  // the language picker for SOAP-specific controls (version, SOAPAction,
  // envelope template) since the language is always XML.
  const isGrpc = mode === 'grpc';
  const isGraphql = mode === 'graphql';
  const isRawLike = mode === 'raw' || isGraphql || mode === 'soap' || isGrpc;
  const isSoap = mode === 'soap';
  // The Body-tab dropdown only chooses among the four HTTP body shapes now
  // (none/raw/urlencoded/formdata) — GraphQL/SOAP/gRPC are chosen via the
  // protocol switcher above, so hide the dropdown entirely rather than
  // show a control with nothing left to decide.
  $('#bodyMode').classList.toggle('hidden', isGraphql || isSoap || isGrpc);
  $('#bodyRawWrap').classList.toggle('hidden', !isRawLike);
  $('#bodyRaw').placeholder = isGraphql ? 'GraphQL query' : 'Request body';
  $('#bodyLanguage').classList.toggle('hidden', !isRawLike || isSoap || isGrpc || isGraphql);
  updatePrettifyVisibility();
  updateBodyRawHighlight();
  $('#soapFields').classList.toggle('hidden', !isSoap);
  $('#grpcFields').classList.toggle('hidden', !isGrpc);
  $('#graphqlFields').classList.toggle('hidden', !isGraphql);
  $('#httpUrlBar').classList.toggle('hidden', isGrpc);
  $('#grpcUrlBar').classList.toggle('hidden', !isGrpc);
  $('#bodyUrlEncodedTable').classList.toggle('hidden', mode !== 'urlencoded');
  $('#addUrlEncodedRow').classList.toggle('hidden', mode !== 'urlencoded');
  $('#bodyFormDataTable').classList.toggle('hidden', mode !== 'formdata');
  $('#addFormDataRow').classList.toggle('hidden', mode !== 'formdata');
  if (mode === 'urlencoded') {
    renderKVTable('bodyUrlEncodedTable', currentRequest.body.urlEncoded || (currentRequest.body.urlEncoded = []));
  }
  if (mode === 'formdata') {
    renderFormDataTable();
  }
  if (isSoap) {
    currentRequest.body.rawLanguage = 'xml';
    checkSoapWellFormed();
  } else if (isGrpc || isGraphql) {
    currentRequest.body.rawLanguage = 'json';
    $('#bodyRawHint').classList.add('hidden');
  } else {
    $('#bodyRawHint').classList.add('hidden');
  }
  $('#wsSecurityCreds').classList.toggle('hidden', !isSoap || !$('#wsSecurityMode').value);
  if (isGrpc) {
    const grpc = currentRequest.body.grpc || (currentRequest.body.grpc = { target: '', plaintext: false, fullMethod: '', metadata: [] });
    $('#grpcTarget').value = grpc.target || '';
    $('#grpcPlaintext').checked = !!grpc.plaintext;
    $('#grpcFullMethod').value = grpc.fullMethod || '';
    renderKVTable('grpcMetadataTable', grpc.metadata || (grpc.metadata = []));
  }
  if (isGraphql) {
    // Only variables, not the query — switching the protocol chip onto an
    // otherwise-blank request should carry over whatever's already typed
    // in the (shared) query textarea rather than clobber it.
    $('#graphqlVariables').value = splitGraphqlBody(currentRequest.body)[1];
  }
}

// Fast, local feedback before a round-trip: is the body even well-formed
// XML? Doesn't validate against the SOAP schema (variables like {{token}}
// would fail that anyway) — just catches typos (unclosed tags, stray &)
// that would otherwise surface as an opaque server-side parse fault.
function checkSoapWellFormed() {
  const hint = $('#bodyRawHint');
  const raw = $('#bodyRaw').value;
  if (!raw.trim()) { hint.classList.add('hidden'); return; }
  const resolved = raw.replace(/\{\{[^}]+\}\}/g, 'x'); // don't let {{vars}} trip the parser
  const doc = new DOMParser().parseFromString(resolved, 'text/xml');
  const err = doc.querySelector('parsererror');
  hint.classList.toggle('hidden', !err);
  hint.classList.toggle('hint-err', !!err);
  if (err) hint.textContent = 'Not well-formed XML: ' + err.textContent.split('\n').filter(Boolean)[0];
}

const SOAP_ENVELOPE_TEMPLATES = {
  '1.1': `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Header/>
  <soap:Body>
    <!-- your request element here -->
  </soap:Body>
</soap:Envelope>`,
  '1.2': `<?xml version="1.0" encoding="utf-8"?>
<soap12:Envelope xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:soap12="http://www.w3.org/2003/05/soap-envelope">
  <soap12:Header/>
  <soap12:Body>
    <!-- your request element here -->
  </soap12:Body>
</soap12:Envelope>`,
};

// Form-data rows need a Type (text/file) selector the generic KV table
// doesn't have, so this is its own renderer rather than reusing
// renderKVTable. Browsers never expose a real filesystem path from a
// <input type="file"> picker, and hapidays is a local, loopback-only
// server (same trust model as the client-cert path fields in Settings) —
// so a "file" field stores a path the backend reads at send time
// (execute.go's buildBody), rather than shipping file bytes over the API.
function renderFormDataTable() {
  const container = $('#bodyFormDataTable');
  const list = currentRequest.body.formData || (currentRequest.body.formData = []);
  container.innerHTML = '';
  list.forEach((f, i) => {
    const row = document.createElement('div');
    row.className = 'kv-row';
    row.innerHTML = `
      <input type="checkbox" ${f.disabled ? '' : 'checked'} title="Enabled">
      <input type="text" placeholder="Key" value="${escapeAttr(f.key)}">
      <input type="text" placeholder="Value" value="${escapeAttr(f.value)}">
      <select style="flex:0 0 70px">
        <option value="text">Text</option>
        <option value="file">File</option>
      </select>
      <button class="remove-row" title="Remove">×</button>`;
    const [chk, keyInput, valInput] = row.querySelectorAll('input');
    const typeSel = row.querySelector('select');
    typeSel.value = f.type === 'file' ? 'file' : 'text';
    chk.onchange = () => { f.disabled = !chk.checked; };
    keyInput.oninput = () => { f.key = keyInput.value; };
    const applyTypeState = () => {
      f.type = typeSel.value;
      if (f.type === 'file') {
        valInput.value = f.src || '';
        valInput.placeholder = 'Local file path, e.g. /Users/you/file.pdf';
        valInput.oninput = () => { f.src = valInput.value; };
      } else {
        valInput.value = f.value || '';
        valInput.placeholder = 'Value';
        valInput.oninput = () => { f.value = valInput.value; };
      }
    };
    typeSel.onchange = applyTypeState;
    applyTypeState();
    row.querySelector('.remove-row').onclick = () => { list.splice(i, 1); renderFormDataTable(); };
    container.appendChild(row);
  });
}

function collectFormIntoRequest() {
  // The method select has no GRPC option (it's an HTTP verb list) — for a
  // gRPC request, leave currentRequest.method as whatever the importer set
  // ("GRPC") rather than clobbering it with GET/POST from a select the
  // gRPC UI hides.
  const bodyMode = $('#bodyMode').value;
  if (bodyMode !== 'grpc') {
    currentRequest.method = $('#methodSelect').value;
  } else if (currentRequest.method !== 'GRPC') {
    currentRequest.method = 'GRPC';
  }
  // urlRaw is kept live-updated by composeUrlFromFields() on every
  // Protocol/Domain/Port/Path edit — nothing to re-read here.
  currentRequest.body.mode = bodyMode;
  currentRequest.body.rawLanguage = $('#bodyLanguage').value;
  if (bodyMode === 'graphql') {
    currentRequest.body.graphqlQuery = $('#bodyRaw').value;
    currentRequest.body.graphqlVariables = $('#graphqlVariables').value;
  } else {
    currentRequest.body.raw = $('#bodyRaw').value;
  }
  currentRequest.body.soapVersion = $('#soapVersion').value;
  currentRequest.body.soapAction = $('#soapAction').value;
  currentRequest.body.wsSecurityMode = $('#wsSecurityMode').value;
  currentRequest.body.wsSecurityUsername = $('#wsSecurityUsername').value;
  currentRequest.body.wsSecurityPassword = $('#wsSecurityPassword').value;
  currentRequest.body.signBody = $('#signBody').checked;
  if (currentRequest.body.mode === 'grpc') {
    const grpc = currentRequest.body.grpc || (currentRequest.body.grpc = {});
    grpc.target = $('#grpcTarget').value;
    grpc.plaintext = $('#grpcPlaintext').checked;
    grpc.fullMethod = $('#grpcFullMethod').value;
    grpc.requestJson = $('#bodyRaw').value;
  }
  return currentRequest;
}

// ---------- send ----------

// Reads the sidebar's TLS override select. Backend fields (sendRequest/
// runRequest's InsecureSkipVerify *bool) are session-scoped overrides,
// not part of the saved RequestSpec — undefined here correctly omits the
// field so the server falls back to the global Settings toggle.
function currentInsecureSkipVerifyOverride() {
  if (state.tlsOverride === 'skip') return true;
  if (state.tlsOverride === 'enforce') return false;
  return undefined;
}

function updateTlsWarnDot() {
  $('#tlsWarnDot').classList.toggle('hidden', state.tlsOverride !== 'skip');
}

// The most recent Send, kept so it can be saved as test evidence.
let lastEvidenceSource = null;

function currentRequestName() {
  try {
    if (state.currentCollection && state.selectedPath) return getNodeAt(state.selectedPath).name;
  } catch (_) { /* stale path — fall through to the URL */ }
  return `${currentRequest.method} ${currentRequest.urlRaw}`;
}

async function sendRequest() {
  collectFormIntoRequest();
  lastEvidenceSource = null;
  $('#saveEvidenceBtn').classList.add('hidden');
  $('#responseStatus').textContent = 'Sending…';
  $('#responseStatus').className = 'response-status';
  $('#respMeta').classList.add('hidden');
  setAssertPill(null);
  $('#responseBody').textContent = '';
  try {
    const result = await api('/send', {
      method: 'POST',
      body: JSON.stringify({
        request: currentRequest,
        collectionId: state.currentCollection ? state.currentCollection.id : '',
        environmentId: state.currentEnvironmentId,
        insecureSkipVerify: currentInsecureSkipVerifyOverride(),
      }),
    });
    renderResponse(result);
    lastEvidenceSource = { name: currentRequestName(), result };
    $('#saveEvidenceBtn').classList.remove('hidden');
    if (result.captured && Object.keys(result.captured).length) {
      await loadEnvironments();
    }
    loadHistory();
  } catch (e) {
    $('#responseStatus').textContent = 'Error: ' + e.message;
    $('#responseStatus').className = 'response-status status-err';
  }
}

// Regex-based tokenizer, not a real parser — good enough for coloring text
// JSON.stringify already produced (see renderResponse), not for validating
// arbitrary input. Escapes each piece itself rather than escaping the whole
// string upfront, since escapeHtml turns `"` into `&quot;` and would break
// the token regex's own quote matching.
function highlightJson(text) {
  const tokenRe = /"(?:\\.|[^"\\])*"(?:\s*:)?|\b(?:true|false|null)\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g;
  let out = '';
  let lastIndex = 0;
  let m;
  while ((m = tokenRe.exec(text)) !== null) {
    out += escapeHtml(text.slice(lastIndex, m.index));
    const token = m[0];
    let cls = 'tok-num';
    if (token[0] === '"') cls = /:\s*$/.test(token) ? 'tok-key' : 'tok-str';
    else if (token === 'true' || token === 'false') cls = 'tok-bool';
    else if (token === 'null') cls = 'tok-null';
    out += `<span class="${cls}">${escapeHtml(token)}</span>`;
    lastIndex = tokenRe.lastIndex;
  }
  out += escapeHtml(text.slice(lastIndex));
  return out;
}

// Colors one XML/SOAP tag (already isolated by highlightXml's tokenizer) —
// splits it into the angle bracket(s), the element name, and each
// name="value" attribute pair, escaping each piece itself for the same
// reason highlightJson does.
function highlightXmlTag(tag) {
  const m = tag.match(/^<(\/)?([\w:.-]+)/);
  if (!m) return escapeHtml(tag);
  let rest = tag.slice(m[0].length);
  let selfClose = false;
  if (/\/>\s*$/.test(rest)) { selfClose = true; rest = rest.slice(0, rest.lastIndexOf('/')); }
  else { rest = rest.slice(0, -1); } // drop trailing '>'
  let out = '<span class="tok-punc">&lt;' + (m[1] ? '/' : '') + '</span>'
    + '<span class="tok-tag">' + escapeHtml(m[2]) + '</span>';
  const attrRe = /([\w:.-]+)(\s*=\s*)("[^"]*"|'[^']*')/g;
  let lastIndex = 0, am;
  while ((am = attrRe.exec(rest)) !== null) {
    out += escapeHtml(rest.slice(lastIndex, am.index));
    out += '<span class="tok-attr">' + escapeHtml(am[1]) + '</span>' + escapeHtml(am[2])
      + '<span class="tok-str">' + escapeHtml(am[3]) + '</span>';
    lastIndex = attrRe.lastIndex;
  }
  out += escapeHtml(rest.slice(lastIndex));
  out += '<span class="tok-punc">' + (selfClose ? '/&gt;' : '&gt;') + '</span>';
  return out;
}

// Same tokenizer shape as formatXml (comments/PIs/CDATA/tags/text), reused
// here to color instead of re-indent.
function highlightXml(text) {
  const tokenRe = /<!--[\s\S]*?-->|<\?[\s\S]*?\?>|<!\[CDATA\[[\s\S]*?\]\]>|<\/?[^>]+>|[^<]+/g;
  let out = '';
  let m;
  while ((m = tokenRe.exec(text)) !== null) {
    const token = m[0];
    if (token.startsWith('<!--') || token.startsWith('<?')) {
      out += '<span class="tok-comment">' + escapeHtml(token) + '</span>';
    } else if (token.startsWith('<')) {
      out += highlightXmlTag(token);
    } else {
      out += escapeHtml(token);
    }
  }
  return out;
}

function setAssertPill(assertions) {
  const pill = $('#respAssertPill');
  if (!assertions || assertions.length === 0) {
    pill.classList.add('hidden');
    return;
  }
  const passed = assertions.filter(a => a.passed).length;
  const allOk = passed === assertions.length;
  pill.classList.remove('hidden');
  pill.className = 'assert-pill ' + (allOk ? 'assert-ok' : 'assert-fail');
  pill.textContent = `${passed}/${assertions.length} assertions passed`;
}

function renderResponse(result) {
  if (result.error) {
    $('#responseStatus').textContent = `Error: ${result.error} (${result.durationMs}ms)`;
    $('#responseStatus').className = 'response-status status-err';
    $('#respMeta').classList.add('hidden');
    setAssertPill(null);
    $('#responseBody').textContent = '';
    renderAssertionSummary(null);
    return;
  }
  renderAssertionSummary(result.assertions);
  setAssertPill(result.assertions);
  const fault = !result.bodyIsBase64 ? detectSoapFault(result.body) : null;
  const statusClass = fault ? 'status-err' : 'status-' + Math.floor(result.status / 100);
  $('#responseStatus').className = 'response-status ' + statusClass;
  $('#responseStatus').textContent = fault
    ? `SOAP Fault${fault.label ? ' (' + fault.label + ')' : ''}: ${fault.message} · HTTP ${result.status}`
    // statusText is Go's resp.Status ("200 OK" — code and reason phrase
    // together, see execute.go), so it already carries the code; prepending
    // result.status again produced "200 200 OK".
    : (result.statusText || String(result.status));
  $('#respMeta').classList.remove('hidden');
  $('#respMeta').textContent = `${result.durationMs} ms · ${result.sizeBytes} B`;

  let bodyText = result.bodyIsBase64 ? '(binary response, base64)\n' + result.body : result.body;
  let isJson = false;
  let isXml = false;
  if (!result.bodyIsBase64) {
    try {
      bodyText = JSON.stringify(JSON.parse(result.body), null, 2);
      isJson = true;
    } catch (_) {
      if (looksLikeXml(result.body, result.headers)) {
        bodyText = formatXml(result.body);
        isXml = true;
      }
    }
  }
  if (isJson) {
    $('#responseBody').innerHTML = highlightJson(bodyText);
  } else if (isXml) {
    $('#responseBody').innerHTML = highlightXml(bodyText);
  } else {
    $('#responseBody').textContent = bodyText;
  }

  const headersDiv = $('#responseHeaders');
  headersDiv.innerHTML = '';
  for (const [k, values] of Object.entries(result.headers || {})) {
    const line = document.createElement('div');
    line.textContent = `${k}: ${values.join(', ')}`;
    headersDiv.appendChild(line);
  }
}

async function saveCurrentRequest() {
  if (!state.currentCollection || !state.selectedPath) {
    alert('Select a request in a collection first (or create a collection).');
    return;
  }
  collectFormIntoRequest();
  const node = getNodeAt(state.selectedPath);
  node.request = currentRequest;
  state.currentCollection = await api(`/collections/${state.currentCollection.id}`, {
    method: 'PUT', body: JSON.stringify(state.currentCollection),
  });
  lastSavedSnapshot = JSON.stringify(currentRequest);
  $('#dirtyDot').classList.add('hidden');
  renderCollectionList();
}

// ---------- environments ----------

async function loadEnvironments() {
  state.environments = (await api('/environments')) || [];
  const sel = $('#envSelect');
  const prev = sel.value;
  sel.innerHTML = '<option value="">No environment</option>';
  for (const env of state.environments) {
    const opt = document.createElement('option');
    opt.value = env.id; opt.textContent = env.name;
    sel.appendChild(opt);
  }
  sel.value = state.environments.some(e => e.id === prev) ? prev : '';
  state.currentEnvironmentId = sel.value;
}

function openEnvEditor() {
  const env = state.environments.find(e => e.id === state.currentEnvironmentId);
  const values = env ? JSON.parse(JSON.stringify(env.values)) : [];
  // editingId tracks which environment Save overwrites — null means Save
  // creates a new one instead. Starts as env's id (editing in place);
  // clicking Duplicate below clears it so Save creates a fresh copy
  // rather than overwriting the one you duplicated from. This is how you
  // build a Test environment out of Dev without retyping every variable:
  // open Dev, Duplicate, rename, tweak the couple of values that differ.
  let editingId = env ? env.id : null;

  showModal(`
    <h3>Environment</h3>
    <p class="hint">The values that actually differ between Dev/QA/Prod — host, port, credentials, tenant IDs.
    Anything the same across all of them belongs in the collection's Variables/Headers instead, so switching
    environments is all it takes.</p>
    <div class="field-row"><label>Name</label><input type="text" id="envNameInput" value="${escapeAttr(env ? env.name : 'New Environment')}"></div>
    <div id="envValuesTable" class="kv-table"></div>
    <button class="add-row" id="envAddRow">+ Add variable</button>
    <div class="field-row"><label>Client cert (mTLS override)</label><input type="text" id="envClientCert" placeholder="leave blank to use the global Settings cert" value="${escapeAttr(env ? env.clientCertFile || '' : '')}"></div>
    <div class="field-row"><label>Client key (mTLS override)</label><input type="text" id="envClientKey" placeholder="leave blank to use the global Settings cert" value="${escapeAttr(env ? env.clientKeyFile || '' : '')}"></div>
    <p class="hint">Only needed if this environment (e.g. prod) uses a different client certificate than the one configured
    in Settings. Leave both blank to fall back to the global cert.</p>
    <div class="modal-actions">
      <button id="envCancel">Cancel</button>
      ${env ? '<button id="envDuplicate" title="Start a new environment pre-filled with these variables">Duplicate</button>' : ''}
      <button id="envSave" style="background:var(--accent);color:#fff">Save</button>
    </div>
  `);

  const renderEnvTable = () => renderKVTable('envValuesTable', values);
  renderEnvTable();
  $('#envAddRow').onclick = () => { values.push({ key: '', value: '', disabled: false }); renderEnvTable(); };
  $('#envCancel').onclick = closeModal;
  if (env) {
    $('#envDuplicate').onclick = () => {
      editingId = null;
      $('#envNameInput').value = env.name + ' copy';
      $('#envDuplicate').remove();
    };
  }
  $('#envSave').onclick = async () => {
    const payload = {
      id: editingId || '', name: $('#envNameInput').value, values,
      clientCertFile: $('#envClientCert').value,
      clientKeyFile: $('#envClientKey').value,
    };
    const saved = editingId
      ? await api(`/environments/${editingId}`, { method: 'PUT', body: JSON.stringify(payload) })
      : await api(`/environments/${crypto.randomUUID()}`, { method: 'PUT', body: JSON.stringify(payload) });
    await loadEnvironments();
    $('#envSelect').value = saved.id;
    state.currentEnvironmentId = saved.id;
    closeModal();
  };
}

// ---------- history ----------

async function loadHistory() {
  const entries = (await api('/history')) || [];
  const container = $('#historyList');
  container.innerHTML = '';
  entries.slice().reverse().slice(0, 100).forEach(h => {
    const div = document.createElement('div');
    div.className = 'history-item';
    const statusLabel = h.status ? h.status : 'ERR';
    div.textContent = `${h.method} ${statusLabel} · ${h.url}`;
    div.title = new Date(h.timestamp).toLocaleString() + ' — click to reload into the editor';
    div.onclick = () => loadHistoryEntry(h);
    container.appendChild(div);
  });
}

// Pops a history entry back into the request editor so it can be
// inspected or re-sent. Best-effort restores the collection/environment
// it originally ran under too (they may since have been deleted/renamed),
// since that's what "inherit" auth and {{vars}} resolve against — without
// it, replaying a history entry for a request that used inherited auth
// would silently send with no auth, the exact bug this app used to have.
async function loadHistoryEntry(entry) {
  if (entry.collectionId && (!state.currentCollection || state.currentCollection.id !== entry.collectionId)) {
    try {
      state.currentCollection = await api(`/collections/${entry.collectionId}`);
    } catch (e) {
      state.currentCollection = null; // collection no longer exists — reload the request anyway
    }
  }
  if (entry.environmentId && state.environments.some(e => e.id === entry.environmentId)) {
    state.currentEnvironmentId = entry.environmentId;
    $('#envSelect').value = entry.environmentId;
  }
  state.selectedPath = null; // not tied to a saved tree node — Send works, Save needs a node picked first
  renderCollectionList();
  loadRequestIntoForm(entry.request);
}

// ---------- collection runner ----------

// Shared by "Run" and "Step through…" — both read the same iteration-data
// file/count fields on the Run modal, so a data set entered once works
// either way.
async function readIterationDataFromRunModal() {
  let dataRows = null;
  const file = $('#runDataFile').files[0];
  if (file) dataRows = await parseDataFile(file);
  const iterations = parseInt($('#runIterations').value, 10) || 1;
  if (!dataRows && iterations > 1) dataRows = Array.from({ length: iterations }, () => ({}));
  return dataRows;
}

function openRunModal(collectionId, folderId, label) {
  showModal(`
    <h3>Run: ${escapeHtml(label)}</h3>
    <div class="field-row"><label>Iteration data (optional) — CSV or JSON array of objects, one row/object per run</label>
      <input type="file" id="runDataFile" accept=".csv,.json"></div>
    <div class="field-row"><label>Iterations (used only if no data file is given)</label>
      <input type="text" id="runIterations" value="1"></div>
    <div class="field-row"><label>Delay between requests (ms)</label>
      <input type="text" id="runDelay" value="0"></div>
    <div class="modal-actions">
      <button id="runCancel">Cancel</button>
      <button id="runStep">Step through…</button>
      <button id="runStart" style="background:var(--accent);color:#fff">Run</button>
    </div>
    <div id="runResults"></div>
  `);
  $('#runCancel').onclick = closeModal;
  $('#runStep').onclick = async () => {
    const dataRows = await readIterationDataFromRunModal();
    const delayMs = parseInt($('#runDelay').value, 10) || 0;
    openStepModal(collectionId, folderId, label, dataRows, delayMs);
  };
  $('#runStart').onclick = async () => {
    $('#runStart').disabled = true;
    $('#runStart').textContent = 'Running…';
    try {
      const dataRows = await readIterationDataFromRunModal();

      const results = await api('/run', {
        method: 'POST',
        body: JSON.stringify({
          collectionId, folderId: folderId || undefined,
          environmentId: state.currentEnvironmentId,
          dataRows, delayMs: parseInt($('#runDelay').value, 10) || 0,
          insecureSkipVerify: currentInsecureSkipVerifyOverride(),
          includeExchanges: true, // so a result can be saved as test evidence
        }),
      });
      renderRunResults(results, collectionId);
    } catch (e) {
      $('#runResults').textContent = 'Run failed: ' + e.message;
    } finally {
      $('#runStart').disabled = false;
      $('#runStart').textContent = 'Run';
    }
  };
}

// ---------- OData $batch ----------

function openBatchModal(collectionId, folderId, label) {
  // If this collection came from the OData importer, "baseUrl" already
  // holds the service root — batchUrl is almost always just that + /$batch.
  const baseVar = (state.currentCollection.variables || []).find(v => v.key === 'baseUrl');
  const guessedBatchUrl = baseVar ? baseVar.value.replace(/\/$/, '') + '/$batch' : '';

  showModal(`
    <h3>Send as $batch: ${escapeHtml(label)}</h3>
    <p class="hint">Bundles every request in this folder into one OData $batch HTTP call (multipart/mixed —
    GET requests sent directly, everything else wrapped in its own changeset) and shows the individual
    sub-responses.</p>
    <div class="field-row"><label>$batch URL</label><input type="text" id="batchUrlInput" value="${escapeAttr(guessedBatchUrl)}" placeholder="{{baseUrl}}/\$batch"></div>
    <div class="modal-actions">
      <button id="batchCancel">Cancel</button>
      <button id="batchStart" style="background:var(--accent);color:#fff">Send</button>
    </div>
    <div id="batchResults"></div>
  `);
  $('#batchCancel').onclick = closeModal;
  $('#batchStart').onclick = async () => {
    const batchUrl = $('#batchUrlInput').value.trim();
    if (!batchUrl) { alert('Enter the $batch URL.'); return; }
    $('#batchStart').disabled = true;
    $('#batchStart').textContent = 'Sending…';
    try {
      const results = await api('/odata/batch', {
        method: 'POST',
        body: JSON.stringify({ collectionId, folderId: folderId || undefined, environmentId: state.currentEnvironmentId, batchUrl }),
      });
      renderBatchResults(results);
    } catch (e) {
      $('#batchResults').textContent = 'Batch failed: ' + e.message;
    } finally {
      $('#batchStart').disabled = false;
      $('#batchStart').textContent = 'Send';
    }
  };
}

function renderBatchResults(results) {
  const passCount = results.filter(r => !r.error && r.status >= 200 && r.status < 300).length;
  const container = $('#batchResults');
  container.innerHTML = '';
  const summary = document.createElement('p');
  summary.textContent = `${passCount}/${results.length} sub-requests returned 2xx`;
  container.appendChild(summary);
  const table = document.createElement('div');
  table.className = 'kv-table';
  results.forEach(r => {
    const row = document.createElement('div');
    row.className = 'kv-row';
    const ok = !r.error && r.status >= 200 && r.status < 300;
    const bodyPreview = (r.body || '').slice(0, 300);
    row.innerHTML = `<div style="flex:1">
      <div style="color:${ok ? 'var(--ok)' : 'var(--danger)'}">${escapeHtml(r.method)} ${escapeHtml(r.error || String(r.status))} · ${escapeHtml(r.name)}</div>
      <pre style="white-space:pre-wrap;word-break:break-word;font-size:11px;margin:4px 0 0">${escapeHtml(bodyPreview)}</pre>
    </div>`;
    table.appendChild(row);
  });
  container.appendChild(table);
}

// Whether a request "passed". If it has assertions they are the test and
// decide the outcome on their own — so a deliberate failure case ("expect
// 403", "expect 500") shows green when it fails the way it should. Only a
// request with no assertions falls back to "got a 2xx/3xx and no transport
// error". A transport error is never a pass.
function stepPassed({ error, status, assertions }) {
  if (error) return false;
  if (assertions && assertions.length > 0) return assertions.every(a => a.passed);
  return status >= 200 && status < 400;
}

function renderRunResults(results, collectionId) {
  const stepOk = (r) => stepPassed(r);
  // Iteration data gives each pass over the requests its own row; name the
  // evidence entries so passes stay distinguishable.
  const multiRow = results.some(r => r.iteration > 0);
  const evidenceEntry = (r) => ({ name: (multiRow ? `[Row ${r.iteration + 1}] ` : '') + r.name, result: r.exchange });
  const saveable = results.filter(r => r.exchange);
  const passCount = results.filter(stepOk).length;
  const anyAssertions = results.some(r => r.assertions && r.assertions.length > 0);
  const container = $('#runResults');
  container.innerHTML = '';
  const summary = document.createElement('p');
  summary.textContent = `${passCount}/${results.length} passed (a request with assertions passes when they all pass; otherwise it needs a 2xx/3xx${anyAssertions ? '' : ' and no transport error'})`;
  container.appendChild(summary);
  if (saveable.length) {
    const saveAll = document.createElement('button');
    saveAll.textContent = 'Save all as evidence';
    saveAll.title = 'Save every request and response from this run as test evidence';
    saveAll.style.marginBottom = '6px';
    saveAll.onclick = () => openSaveEvidenceModal(saveable.map(evidenceEntry), { collectionId });
    container.appendChild(saveAll);
  }
  const table = document.createElement('div');
  table.className = 'kv-table';
  results.forEach(r => {
    const row = document.createElement('div');
    row.className = 'kv-row';
    const ok = stepOk(r);
    const assertBadge = r.assertions && r.assertions.length
      ? ` · assertions ${r.assertions.filter(a => a.passed).length}/${r.assertions.length}`
      : '';
    row.innerHTML = `<span style="flex:1;color:${ok ? 'var(--ok)' : 'var(--danger)'}">${escapeHtml(String(r.iteration))} · ${escapeHtml(r.method)} ${escapeHtml(r.error || String(r.status))} · ${r.durationMs}ms · ${escapeHtml(r.name)}${assertBadge}</span>`;
    if (r.exchange) {
      const save = document.createElement('button');
      save.textContent = 'Save';
      save.title = 'Save this request and response as test evidence';
      save.onclick = () => openSaveEvidenceModal([evidenceEntry(r)], { collectionId });
      row.appendChild(save);
    }
    table.appendChild(row);
  });
  container.appendChild(table);
}

// ---------- step-through runner ----------

// Depth-first leaf order, matching runner.go's flatten() exactly (folder
// vs. request is told apart by `request` being present, not by `children`
// being absent — an empty folder still has children:[], not children:null,
// since the omitempty fix) so step-through and "Run" agree on order.
function flattenNodesForStep(nodes) {
  let out = [];
  for (const n of nodes) {
    if (n.request) out.push(n);
    else if (n.children) out = out.concat(flattenNodesForStep(n.children));
  }
  return out;
}

function findNodeById(nodes, id) {
  for (const n of nodes) {
    if (n.id === id) return n;
    if (n.children) {
      const found = findNodeById(n.children, id);
      if (found) return found;
    }
  }
  return null;
}

async function openStepModal(collectionId, folderId, label, dataRows, delayMs) {
  // Fetched fresh rather than read off state.currentCollection: the ▶/Step
  // icon on a collection row works even when that collection isn't the
  // currently-open one.
  const col = await api(`/collections/${collectionId}`);
  const rootNodes = folderId ? (findNodeById(col.root, folderId)?.children || []) : col.root;
  const baseSteps = flattenNodesForStep(rootNodes);

  // One full pass through baseSteps per data row — same order as
  // runner.go's Run() (outer loop over iterations, inner loop over
  // requests). `row: null` (no data file/iteration count given) means
  // "no extra var overrides", same as a single implicit run.
  const rows = (dataRows && dataRows.length) ? dataRows : [null];
  const steps = [];
  rows.forEach((row, rowIdx) => {
    baseSteps.forEach(node => steps.push({ node, row, rowIdx }));
  });
  const multiRow = rows.length > 1;

  showModal(`
    <h3>Step through: ${escapeHtml(label)}</h3>
    <p class="hint" id="stepProgress"></p>
    <div id="stepLog"></div>
    <div class="modal-actions">
      <button id="stepSaveAll" disabled title="Save every step run so far as test evidence">Save all as evidence</button>
      <button id="stepStop">Stop</button>
      <button id="stepRunToEnd">Run to end ⏩</button>
      <button id="stepNext" style="background:var(--accent);color:#fff">Step ▶</button>
    </div>
  `);

  if (baseSteps.length === 0) {
    $('#stepProgress').textContent = 'Nothing to step through — this folder has no requests.';
    $('#stepNext').disabled = true;
    $('#stepRunToEnd').disabled = true;
    $('#stepStop').onclick = closeModal;
    return;
  }

  let idx = 0;
  let stopped = false;
  const log = $('#stepLog');
  const executed = []; // { name, result } for every step that got a response, in order — what "Save all" saves

  const renderProgress = () => {
    if (idx >= steps.length) {
      $('#stepProgress').textContent = `Done — ${steps.length}/${steps.length} steps executed.`;
      $('#stepNext').disabled = true;
      $('#stepRunToEnd').disabled = true;
      $('#stepStop').textContent = 'Close'; // nothing left to stop
      return;
    }
    const step = steps[idx];
    const stepInRow = idx % baseSteps.length;
    const rowLabel = multiRow ? `Row ${step.rowIdx + 1}/${rows.length} · ` : '';
    $('#stepProgress').textContent = `${rowLabel}Step ${stepInRow + 1} of ${baseSteps.length}: ${step.node.name}`;
  };

  // Runs steps[idx], appends an input+output card to the log, advances idx.
  // Shared by "Step" (one call) and "Run to end" (called in a loop).
  const runOneStep = async () => {
    const step = steps[idx];
    const node = step.node;
    const req = node.request;
    const rowTag = multiRow ? `[Row ${step.rowIdx + 1}] ` : '';
    const card = document.createElement('div');
    card.className = 'step-card';
    card.innerHTML = `
      <div class="step-card-title">${idx + 1}. ${escapeHtml(rowTag + node.name)}</div>
      <div class="step-card-input">→ ${escapeHtml(req.method)} ${escapeHtml(req.urlRaw)}${req.auth && req.auth.type && req.auth.type !== 'none' ? ' · auth: ' + escapeHtml(req.auth.type) : ''}${req.body && req.body.mode !== 'none' ? ' · body: ' + escapeHtml(req.body.mode) : ''}${step.row ? ' · row vars: ' + escapeHtml(JSON.stringify(step.row)) : ''}</div>
      <div class="step-card-output">Sending…</div>
    `;
    log.appendChild(card);
    card.scrollIntoView({ block: 'end' });

    try {
      const result = await api('/send', {
        method: 'POST',
        body: JSON.stringify({
          request: req, collectionId, environmentId: state.currentEnvironmentId,
          extraVars: step.row || undefined,
          insecureSkipVerify: currentInsecureSkipVerifyOverride(),
        }),
      });
      const ok = stepPassed(result);
      const capturedText = result.captured && Object.keys(result.captured).length
        ? `\ncaptured: ${JSON.stringify(result.captured)}` : '';
      const assertionText = result.assertions && result.assertions.length
        ? '\n' + result.assertions.map(a => `${a.passed ? '✓' : '✗'} ${a.message}`).join('\n') : '';
      const bodyPreview = (result.body || '').slice(0, 500);
      card.querySelector('.step-card-output').innerHTML = `
        <span style="color:${ok ? 'var(--ok)' : 'var(--danger)'}">← ${escapeHtml(result.error || result.statusText || String(result.status))} · ${result.durationMs}ms${result.resolvedUrl ? ' · ' + escapeHtml(result.resolvedUrl) : ''}</span>
        <pre>${escapeHtml(bodyPreview)}${capturedText ? escapeHtml(capturedText) : ''}${assertionText ? escapeHtml(assertionText) : ''}</pre>
      `;
      if (result.captured && Object.keys(result.captured).length) await loadEnvironments();
      const entry = { name: rowTag + node.name, result };
      executed.push(entry);
      $('#stepSaveAll').disabled = false;
      const saveBtn = document.createElement('button');
      saveBtn.textContent = 'Save evidence';
      saveBtn.title = 'Save this step\'s request and response as test evidence';
      saveBtn.style.marginTop = '6px';
      saveBtn.onclick = () => openSaveEvidenceModal([entry], { collectionId });
      card.appendChild(saveBtn);
    } catch (e) {
      card.querySelector('.step-card-output').innerHTML = `<span style="color:var(--danger)">← failed: ${escapeHtml(e.message)}</span>`;
    }
    idx++;
    renderProgress();
  };

  renderProgress();
  $('#stepNext').onclick = async () => {
    if (idx >= steps.length) return;
    $('#stepNext').disabled = true;
    await runOneStep();
    $('#stepNext').disabled = idx >= steps.length;
  };
  $('#stepRunToEnd').onclick = async () => {
    $('#stepNext').disabled = true;
    $('#stepRunToEnd').disabled = true;
    while (idx < steps.length && !stopped) {
      await runOneStep();
      if (delayMs > 0 && idx < steps.length && !stopped) {
        const nextLabel = $('#stepProgress').textContent;
        $('#stepProgress').textContent = `Waiting ${delayMs}ms… next: ${nextLabel}`;
        await new Promise(resolve => setTimeout(resolve, delayMs));
      }
    }
  };
  // "Stop" during a Run-to-end just halts the loop after the in-flight
  // request finishes — the modal stays open showing the log so far.
  $('#stepSaveAll').onclick = () => openSaveEvidenceModal(executed.slice(), { collectionId });
  $('#stepStop').onclick = () => { stopped = true; closeModal(); };
}

async function parseDataFile(file) {
  const text = await file.text();
  if (file.name.endsWith('.json')) {
    return JSON.parse(text);
  }
  const lines = text.split(/\r?\n/).filter(l => l.trim() !== '');
  if (lines.length < 2) return [];
  const headers = lines[0].split(',').map(h => h.trim());
  return lines.slice(1).map(line => {
    const cells = line.split(',');
    const row = {};
    headers.forEach((h, i) => { row[h] = (cells[i] || '').trim(); });
    return row;
  });
}

// ---------- cookies ----------

async function openCookiesModal() {
  const cookies = await api('/cookies');
  showModal(`
    <h3>Cookie Jar</h3>
    <div id="cookiesTable" class="kv-table"></div>

    <h4>Add a cookie</h4>
    <p class="hint">Seed a cookie by hand — e.g. a session value you obtained some other way — rather than only ever accumulating them from responses.</p>
    <div class="field-row"><label>Domain</label><input type="text" id="newCookieDomain" placeholder="api.example.com"></div>
    <div class="field-row"><label>Path</label><input type="text" id="newCookiePath" placeholder="/ (leave blank for every path)"></div>
    <div class="field-row"><label>Name</label><input type="text" id="newCookieName" placeholder="session_id"></div>
    <div class="field-row"><label>Value</label><input type="text" id="newCookieValue" placeholder="value"></div>
    <div class="field-row">
      <label><input type="checkbox" id="newCookieSecure"> Secure</label>
      <label style="margin-left:12px"><input type="checkbox" id="newCookieHttpOnly"> HttpOnly</label>
    </div>
    <button id="cookieAdd">Add cookie</button>

    <div class="modal-actions">
      <button id="cookiesClear">Clear All</button>
      <button id="cookiesClose" style="background:var(--accent);color:#fff">Close</button>
    </div>
  `);
  const table = $('#cookiesTable');
  if (!cookies || cookies.length === 0) {
    table.textContent = 'No cookies stored.';
  } else {
    cookies.forEach(c => {
      const row = document.createElement('div');
      row.className = 'kv-row';
      const flags = [c.secure ? 'Secure' : null, c.httpOnly ? 'HttpOnly' : null].filter(Boolean).join(', ');
      const expires = c.expires && !c.expires.startsWith('0001-01-01') ? ` · expires ${new Date(c.expires).toLocaleString()}` : '';
      row.innerHTML = `<span style="flex:1">${escapeHtml(c.domain)}${escapeHtml(c.path || '')} — ${escapeHtml(c.name)}=${escapeHtml(c.value)}${flags ? ' (' + flags + ')' : ''}${expires}</span>
        <button class="remove-row" title="Remove">×</button>`;
      row.querySelector('.remove-row').onclick = async () => {
        await api(`/cookies/${encodeURIComponent(c.domain)}/${encodeURIComponent(c.name)}`, { method: 'DELETE' });
        openCookiesModal();
      };
      table.appendChild(row);
    });
  }
  $('#cookiesClose').onclick = closeModal;
  $('#cookiesClear').onclick = async () => {
    await api('/cookies', { method: 'DELETE' });
    openCookiesModal();
  };
  $('#cookieAdd').onclick = async () => {
    const domain = $('#newCookieDomain').value.trim();
    const name = $('#newCookieName').value.trim();
    if (!domain || !name) { alert('Domain and name are required.'); return; }
    await api('/cookies', {
      method: 'PUT',
      body: JSON.stringify({
        domain, name,
        path: $('#newCookiePath').value.trim(),
        value: $('#newCookieValue').value,
        secure: $('#newCookieSecure').checked,
        httpOnly: $('#newCookieHttpOnly').checked,
      }),
    });
    openCookiesModal();
  };
}

// ---------- settings ----------

async function openSettings() {
  const settings = await api('/settings');
  showModal(`
    <h3>Settings</h3>
    <div class="field-row"><label>Extra CA bundle (PEM path, for corporate TLS-intercepting proxies)</label>
      <input type="text" id="setCaFile" value="${escapeAttr(settings.extraCaFile || '')}"></div>
    <div class="field-row"><label><input type="checkbox" id="setInsecure" ${settings.insecureSkipVerify ? 'checked' : ''}> Skip TLS certificate verification (insecure)</label></div>
    <div class="field-row"><label>Client certificate path (mTLS)</label>
      <input type="text" id="setClientCert" value="${escapeAttr(settings.clientCertFile || '')}"></div>
    <div class="field-row"><label>Client key path (mTLS)</label>
      <input type="text" id="setClientKey" value="${escapeAttr(settings.clientKeyFile || '')}"></div>
    <div class="field-row"><label>Proxy URL override (blank = use system proxy env vars)</label>
      <input type="text" id="setProxy" value="${escapeAttr(settings.proxyUrl || '')}"></div>

    <h4>This session only</h4>
    <div class="field-row"><label>TLS verification override</label>
      <select id="setTlsOverride">
        <option value="">Use the checkbox above</option>
        <option value="skip">Always skip verification (insecure)</option>
        <option value="enforce">Always enforce verification</option>
      </select>
      <p class="hint">For the odd case the checkbox above doesn't cover well — e.g. you want strict verification
      everywhere else but need to skip it for one throwaway/self-signed target right now, without editing the
      setting above and remembering to undo it. Resets to "use the checkbox above" on restart; not saved with
      Settings below.</p>
    </div>

    <div class="modal-actions">
      <button id="setCancel">Cancel</button>
      <button id="setSave" style="background:var(--accent);color:#fff">Save</button>
    </div>
  `);
  $('#setTlsOverride').value = state.tlsOverride;
  $('#setTlsOverride').onchange = (e) => { state.tlsOverride = e.target.value; updateTlsWarnDot(); };
  $('#setCancel').onclick = closeModal;
  $('#setSave').onclick = async () => {
    await api('/settings', {
      method: 'PUT',
      body: JSON.stringify({
        extraCaFile: $('#setCaFile').value,
        insecureSkipVerify: $('#setInsecure').checked,
        clientCertFile: $('#setClientCert').value,
        clientKeyFile: $('#setClientKey').value,
        proxyUrl: $('#setProxy').value,
      }),
    });
    closeModal();
  };
}

// ---------- modal ----------

function showModal(html) {
  $('#modalContent').innerHTML = html;
  $('#modalOverlay').classList.remove('hidden');
}
function closeModal() {
  $('#modalOverlay').classList.add('hidden');
  $('#modalContent').classList.remove('modal-wide');
}

// ---------- test evidence ----------

const EVIDENCE_WHO_KEY = 'hapidays-evidence-who';

function readEvidenceWho() {
  try { return localStorage.getItem(EVIDENCE_WHO_KEY) || ''; } catch (_) { return ''; }
}

// Saves one or more executed requests as an evidence pack. items is
// [{ name, result }] where result is what /api/send returned. Redaction of
// credentials happens on the server before anything is written.
function showEvidenceModal(html) {
  $('#evidenceContent').innerHTML = html;
  $('#evidenceOverlay').classList.remove('hidden');
}
function closeEvidenceModal() {
  $('#evidenceOverlay').classList.add('hidden');
}

function openSaveEvidenceModal(items, { collectionId } = {}) {
  const n = items.length;
  const colId = collectionId !== undefined ? collectionId : (state.currentCollection ? state.currentCollection.id : '');
  showEvidenceModal(`
    <h3>Save test evidence</h3>
    <p class="hint">${n === 1 ? 'Saves this request and its response' : `Saves these ${n} requests and their responses`},
    with the headers as sent and as received. Credentials are replaced by short fingerprints (the same value always
    gets the same one) unless you tick the box below.</p>
    <div class="field-row"><label for="evWho">Saved by</label>
      <input id="evWho" type="text" style="flex:1" placeholder="Your name" value="${escapeHtml(readEvidenceWho())}"></div>
    <div class="field-row"><label for="evNotes">Notes</label>
      <textarea id="evNotes" rows="4" style="flex:1" placeholder="What was tested, and anything a reviewer should know"></textarea></div>
    <label style="display:block;margin:8px 0"><input type="checkbox" id="evIncludeCreds"> Include credentials
      &mdash; the file will contain real tokens and passwords</label>
    <p class="hint hidden" id="evError" style="color:var(--danger)"></p>
    <div id="evResult"></div>
    <div class="modal-actions">
      <button id="evCancel">Cancel</button>
      <button id="evSave" style="background:var(--accent);color:#fff">Save</button>
    </div>
  `);
  $('#evCancel').onclick = closeEvidenceModal;
  $('#evSave').onclick = async () => {
    const who = $('#evWho').value.trim();
    const err = $('#evError');
    err.classList.add('hidden');
    if (!who) {
      err.textContent = 'Enter who is saving this.';
      err.classList.remove('hidden');
      return;
    }
    try { localStorage.setItem(EVIDENCE_WHO_KEY, who); } catch (_) { /* just won't be remembered */ }
    $('#evSave').disabled = true;
    try {
      const out = await api('/evidence', {
        method: 'POST',
        body: JSON.stringify({
          who,
          notes: $('#evNotes').value,
          includeCredentials: $('#evIncludeCreds').checked,
          collectionId: colId,
          environmentId: state.currentEnvironmentId,
          items: items.map(i => ({ name: i.name, result: i.result })),
        }),
      });
      const base = `/api/evidence/${out.id}`;
      $('#evResult').innerHTML = `
        <p>Saved ${out.items} exchange${out.items === 1 ? '' : 's'}: ${out.passed} passed, ${out.failed} failed.
        ${out.credentialsRedacted ? 'Credentials redacted.' : '<strong>Credentials included.</strong>'}</p>
        <p><a href="${base}/report" target="_blank" rel="noopener">Open report</a> &middot;
        <a href="${base}/text" target="_blank" rel="noopener">Plain text</a> &middot;
        Download: <a href="${base}/text?download=1">.txt</a>,
        <a href="${base}/report?download=1">.html</a></p>`;
      $('#evSave').classList.add('hidden');
      $('#evCancel').textContent = 'Close';
    } catch (e) {
      err.textContent = 'Could not save: ' + e.message;
      err.classList.remove('hidden');
      $('#evSave').disabled = false;
    }
  };
  $('#evWho').focus();
}

// The saved-evidence list: every pack saved so far, newest first, with the
// ways to get at it — open the report, open the plain text, copy the text to
// the clipboard (the usual next step is pasting it into an email or ticket),
// or delete it.
async function openEvidenceListModal() {
  showModal(`
    <h3>Saved test evidence</h3>
    <p class="hint" id="evListHint">Loading&hellip;</p>
    <div id="evList" class="kv-table"></div>
    <div class="modal-actions"><button id="evListClose">Close</button></div>
  `);
  $('#modalContent').classList.add('modal-wide');
  $('#evListClose').onclick = closeModal;
  await renderEvidenceList();
}

async function renderEvidenceList() {
  const hint = $('#evListHint');
  const container = $('#evList');
  let list;
  try {
    list = (await api('/evidence')) || [];
  } catch (e) {
    hint.textContent = 'Could not load the list: ' + e.message;
    return;
  }
  container.innerHTML = '';
  hint.textContent = list.length
    ? `${list.length} saved pack${list.length === 1 ? '' : 's'}, newest first. Save one from the Save evidence button on a response, a step, or a run.`
    : 'Nothing saved yet. Use the Save evidence button on a response, a step, or a run result.';
  for (const p of list) {
    const row = document.createElement('div');
    row.className = 'kv-row';
    row.style.alignItems = 'flex-start';
    const notes = (p.notes || '').replace(/\s+/g, ' ').trim();
    const context = [p.environmentName, p.collectionName].filter(Boolean).join(' · ');
    row.innerHTML = `
      <div style="flex:1;min-width:0">
        <div><strong>${escapeHtml(new Date(p.savedAt).toLocaleString())}</strong> &middot; ${escapeHtml(p.who || '')}</div>
        <div style="font-size:12px">${p.items} exchange${p.items === 1 ? '' : 's'}:
          <span style="color:var(--ok)">${p.passed} passed</span>,
          <span style="color:${p.failed ? 'var(--danger)' : 'inherit'}">${p.failed} failed</span>
          ${context ? '&middot; ' + escapeHtml(context) : ''}
          ${p.credentialsRedacted ? '' : '&middot; <strong style="color:var(--danger)">credentials included</strong>'}</div>
        ${notes ? `<div style="font-size:12px;opacity:.8;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${escapeHtml(notes)}</div>` : ''}
      </div>
      <span class="col-actions" style="display:flex;gap:6px;flex-shrink:0"></span>`;
    const actions = row.querySelector('.col-actions');
    const link = (label, href, title) => {
      const a = document.createElement('a');
      a.textContent = label; a.href = href; a.target = '_blank'; a.rel = 'noopener'; a.title = title;
      a.style.alignSelf = 'center';
      return a;
    };
    actions.appendChild(link('Open', `/api/evidence/${p.id}/report`, 'Open the HTML report in a new tab'));
    actions.appendChild(link('Text', `/api/evidence/${p.id}/text`, 'Open the plain text in a new tab'));
    const copy = document.createElement('button');
    copy.textContent = 'Copy';
    copy.title = 'Copy the plain text to the clipboard, ready to paste';
    copy.onclick = async () => {
      try {
        const res = await fetch(`/api/evidence/${p.id}/text`);
        if (!res.ok) throw new Error(res.statusText);
        await navigator.clipboard.writeText(await res.text());
        copy.textContent = 'Copied ✓';
        setTimeout(() => { copy.textContent = 'Copy'; }, 1500);
      } catch (e) {
        alert('Could not copy: ' + e.message);
      }
    };
    actions.appendChild(copy);
    const del = document.createElement('button');
    del.textContent = 'Delete';
    del.className = 'col-action-danger';
    del.onclick = async () => {
      if (!confirm('Delete this evidence pack? This cannot be undone.')) return;
      try {
        await api(`/evidence/${p.id}`, { method: 'DELETE' });
        await renderEvidenceList();
      } catch (e) {
        alert('Could not delete: ' + e.message);
      }
    };
    actions.appendChild(del);
    container.appendChild(row);
  }
}

// ---------- export ----------

function downloadJson(data, filename) {
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

// Downloads a collection exactly as stored (fetched fresh, not read off
// state, so an export always reflects the last Save even if this isn't
// the currently-open collection). It's the same shape the backend already
// persists to disk, and internal/importer.ImportCollection now recognizes
// this shape on the way back in — "root" at the top level is what tells
// it apart from a Postman export — so Import round-trips it as a new,
// independent collection (fresh IDs throughout) rather than needing a
// separate re-import path.
async function exportCollection(id, name) {
  let col;
  try {
    col = await api(`/collections/${id}`);
  } catch (e) {
    alert('Export failed: ' + e.message);
    return;
  }
  downloadJson(col, (name || 'collection').replace(/[\\/:*?"<>|]/g, '_') + '.hapidays.json');
}

// Downloads a collection rendered as Postman Collection Format v2.1 —
// best-effort (see internal/importer/export.go): WS-Security config and
// gRPC call metadata don't survive, SOAP/gRPC bodies fall back to a raw
// XML/JSON body since Postman has no equivalent mode for either.
async function exportCollectionPostman(id, name) {
  let col;
  try {
    col = await api(`/collections/${id}/export/postman`);
  } catch (e) {
    alert('Export failed: ' + e.message);
    return;
  }
  downloadJson(col, (name || 'collection').replace(/[\\/:*?"<>|]/g, '_') + '.postman_collection.json');
}

// Same idea as exportCollection — internal/importer.ImportEnvironment
// recognizes this shape by the presence of "updatedAt" (every hapidays
// environment has it; a Postman export never does) and reassigns a fresh
// ID on the way back in.
async function exportEnvironment(id, name) {
  let env;
  try {
    env = await api(`/environments/${id}`);
  } catch (e) {
    alert('Export failed: ' + e.message);
    return;
  }
  downloadJson(env, (name || 'environment').replace(/[\\/:*?"<>|]/g, '_') + '.hapidays.json');
}

// ---------- import ----------

async function importFile(input, endpoint, onDone) {
  const file = input.files[0];
  if (!file) return;
  const text = await file.text();
  try {
    await api(endpoint, { method: 'POST', headers: {}, body: text });
    onDone();
  } catch (e) {
    alert('Import failed: ' + e.message);
  }
  input.value = '';
}

// Fetches a hapidays or Postman file from a URL (e.g. a GitHub raw link)
// server-side, the same way importFile does for a local file — see
// importCollectionURL/importEnvironmentURL in internal/api/handlers.go.
function openImportURLModal(endpoint, onDone, opts) {
  opts = opts || {};
  showModal(`
    <h3>${escapeHtml(opts.title || 'Import from URL')}</h3>
    <p class="hint">${opts.hint || 'A hapidays or Postman export hosted somewhere fetchable — e.g. a GitHub raw file link.'}</p>
    <div class="field-row"><label>URL</label><input type="text" id="importUrlInput" style="width:100%"
      placeholder="https://raw.githubusercontent.com/..." value="${escapeAttr(opts.url || '')}"></div>
    <div class="modal-actions">
      <button id="importUrlCancel">Cancel</button>
      <button id="importUrlGo" style="background:var(--accent);color:#fff">Import</button>
    </div>
  `);
  $('#importUrlCancel').onclick = closeModal;
  $('#importUrlGo').onclick = async () => {
    const url = $('#importUrlInput').value.trim();
    if (!url) return;
    try {
      await api(endpoint, { method: 'POST', body: JSON.stringify({ url }) });
      await onDone();
      closeModal();
    } catch (e) {
      alert('Import failed: ' + e.message);
    }
  };
}

// ---------- help ----------

const SMOKE_TEST_URL = 'https://raw.githubusercontent.com/achgithub/hapidays-examples/main/smoke-test.hapidays.json';

function openHelpModal() {
  $('#modalContent').classList.add('modal-wide');
  showModal(`
    <h3>Help</h3>
    <p class="hint">A guide to everything hapidays does. Jump to a section, or just scroll — it's not long
    relative to what it's documenting.</p>

    <nav class="help-nav">
      <a href="#help-model">Collections vs. environments</a>
      <a href="#help-request">Building a request</a>
      <a href="#help-body">Body modes</a>
      <a href="#help-auth">Auth</a>
      <a href="#help-capassert">Captures &amp; assertions</a>
      <a href="#help-collections">Collections</a>
      <a href="#help-environments">Environments</a>
      <a href="#help-running">Sending &amp; running</a>
      <a href="#help-history">History</a>
      <a href="#help-evidence">Test evidence</a>
      <a href="#help-cookies">Cookie jar</a>
      <a href="#help-settings">Settings &amp; network</a>
      <a href="#help-palette">Command palette</a>
      <a href="#help-examples">Example collections</a>
    </nav>

    <div id="help-model" class="help-eyebrow">Model</div>
    <h4>Collections vs. environments</h4>
    <p class="hint">A collection is the <em>shape</em> of an API: its requests, the headers/variables it always
    sends, and the shape of its auth (which type, which header/query param carries it). An environment is the
    <em>values</em> that differ per target, or are sensitive — host, port, tenant URL, and every credential
    (password, token, API key, client secret). A credential should always be a <code>{{var}}</code> referencing
    an environment, never a literal typed into the collection — collections are the thing you export/share,
    environments are the thing you don't. Switching between Dev/QA/Prod should just mean switching the
    environment; nothing about the collection itself should need to change.</p>

    <div id="help-request" class="help-eyebrow">Requests</div>
    <h4>Building a request</h4>
    <p class="hint">The protocol chip above the URL bar (HTTP / SOAP / OData / GraphQL / gRPC) is purely
    descriptive — it drives which fields show up and how the request is tagged in the tree, not how the request
    executes. The URL itself can be typed as one raw string with <code>{{vars}}</code> inline, or composed from
    separate domain/port/path fields that stay in sync with it. Query params, Headers, Body, Auth, Captures, and
    Assertions are their own tabs, each badged with a count when it has content, so a request with something set
    in a tab you're not looking at is still visible at a glance.</p>

    <div id="help-body" class="help-eyebrow">Requests</div>
    <h4>Body modes</h4>
    <dl class="help-dl">
      <dt>Raw</dt><dd>JSON / XML / text, syntax-highlighted while editing (JSON/XML only) with a Prettify
        button to reformat in place.</dd>
      <dt>URL-encoded</dt><dd>A key/value form body, sent as <code>application/x-www-form-urlencoded</code>.</dd>
      <dt>Form-data</dt><dd>Multipart form fields, including file fields.</dd>
      <dt>GraphQL</dt><dd>Query/variables editor, posted as the standard <code>{"query","variables"}</code> JSON body.</dd>
      <dt>SOAP</dt><dd>1.1 or 1.2 envelope, with an optional WS-Security UsernameToken (plaintext or digest
        password) and X.509 message signing (signs the Body using the mTLS client cert configured in Settings).</dd>
      <dt>gRPC</dt><dd>Target host:port, plaintext/TLS toggle, full method, a protojson request editor, and
        outgoing metadata — built automatically by the gRPC reflection importer, or filled in by hand.</dd>
    </dl>

    <div id="help-auth" class="help-eyebrow">Requests</div>
    <h4>Auth tab</h4>
    <p class="hint">Every request has its own Auth tab, independent of its body. <strong>Inherit from
    collection</strong> falls back to whatever the open collection's own Auth is set to (there's no per-folder
    auth) — set it once on the collection and every request that inherits picks it up.</p>
    <dl class="help-dl">
      <dt>Basic / Bearer / Digest</dt><dd>Digest handles the challenge/response round trip transparently — fill
        in username/password and send, same as any other type.</dd>
      <dt>API Key</dt><dd>A named header or query param, your choice of placement.</dd>
      <dt>AWS Signature (SigV4)</dt><dd>Access key, secret key, optional session token (for temporary STS
        credentials), region, service.</dd>
      <dt>OAuth 2.0</dt><dd>Client Credentials, Username &amp; Password, or Authorization Code (opens a browser
        tab, listens on a one-shot local port for the redirect). <strong>Get New Access Token</strong> fetches
        one; once a grant returns a refresh token, a <strong>Refresh Token</strong> button appears so you don't
        need to re-run the whole browser flow just because the token expired.</dd>
    </dl>

    <div id="help-capassert" class="help-eyebrow">Requests</div>
    <h4>Captures &amp; assertions</h4>
    <p class="hint"><strong>Captures</strong> pull a value out of the response — a header, or a dotted JSON path
    like <code>data.token</code> — into an environment variable, for the common "fetch a token, reuse it on the
    next request" pattern, without needing a scripting engine. If you imported a Postman collection whose
    pre-request/test scripts do this with <code>pm.environment.set(...)</code>, <strong>Suggest Captures</strong>
    (on the Captures tab) recognizes the common inline forms and offers to convert them into Capture rules
    automatically.</p>
    <p class="hint"><strong>Assertions</strong> are pass/fail checks against the response, evaluated without a
    scripting engine: status code equals or in a range, a header equals/exists, the body contains a substring, a
    JSON path equals a value or exists, or the response came back under a max duration. These are what let the
    collection runner report pass/fail per request instead of just "here's what came back." When a request has
    assertions, they alone decide whether it passed — so a deliberate failure case (assert status 403) shows green
    when it fails the way you expect. A request with no assertions passes if it gets a 2xx/3xx.</p>

    <div id="help-collections" class="help-eyebrow">Organizing</div>
    <h4>Collections</h4>
    <p class="hint">A tree of folders and requests. The <strong>New</strong> menu creates a request/folder/
    collection by hand, or imports one: a Postman collection file, WSDL (SOAP), OData <code>$metadata</code>,
    GraphQL introspection, gRPC server reflection, an <strong>OpenAPI 3.0/3.1 or Swagger 2.0</strong> document
    (JSON or YAML — generates a folder per tag, one request per operation, and a matching Environment per
    server the spec declares), a pasted curl command, or <strong>Import from URL</strong> — fetches a hapidays
    or Postman file from any reachable link, server-side, the same mechanism WSDL-by-URL import uses (so a
    shared example doesn't need downloading by hand first).</p>
    <p class="hint">The <strong>⚙</strong> icon on a collection opens its settings — Headers and Variables sent
    by default to every request in it (a request can override a header by declaring one with the same name), and
    the collection's own Auth. <strong>⬇</strong> exports it as hapidays's native JSON (round-trips losslessly,
    re-importable on this machine or another); <strong>⬇P</strong> exports it as a Postman v2.1 file instead,
    best-effort — SOAP/gRPC bodies fall back to raw XML/JSON since Postman has no equivalent mode, and
    hapidays's own collection-level Headers have no Postman equivalent so they're dropped on that path only.</p>

    <div id="help-environments" class="help-eyebrow">Organizing</div>
    <h4>Environments</h4>
    <p class="hint">A named set of variable values, plus an optional mTLS client cert/key override for when one
    environment (e.g. prod) needs a different client identity than the one configured globally in Settings. The
    dropdown in the sidebar switches the active one; <strong>Duplicate</strong> (when editing an existing
    environment) starts a new one pre-filled with the same variables — the fast way to build a Test environment
    out of Dev without retyping everything. Import/Export mirror the collection versions (file or URL).</p>

    <div id="help-running" class="help-eyebrow">Executing</div>
    <h4>Sending &amp; running</h4>
    <dl class="help-dl">
      <dt>Send</dt><dd>Runs the one open request against the active environment.</dd>
      <dt>Run (▶ on a collection or folder)</dt><dd>Runs every request in it, in tree order. Optionally once
        per row of a CSV or JSON-array data file (each row's keys become variables for that iteration, taking
        precedence over the collection/environment ones), with a configurable delay between requests. Doesn't
        stop on a failed request — a run is meant to survey a whole collection, not bail on the first 4xx.</dd>
      <dt>Step through…</dt><dd>The same run, but one request at a time with a Next button — for watching what
        each step actually sent/got back instead of only seeing the final summary.</dd>
      <dt>Send as $batch</dt><dd>(OData collections) Bundles every request in a folder into one
        <code>multipart/mixed</code> $batch HTTP call — GET requests sent directly, state-changing ones wrapped
        in their own changeset — and shows the individual sub-responses.</dd>
    </dl>

    <div id="help-history" class="help-eyebrow">Executing</div>
    <h4>History</h4>
    <p class="hint">Every send is logged — method, URL, status, duration, size — and can be reopened back into
    the editor to inspect or re-send. Best-effort restores the collection/environment it ran under too, so
    <code>{{vars}}</code> resolve the same way they did the first time.</p>

    <div id="help-evidence" class="help-eyebrow">Executing</div>
    <h4>Test evidence</h4>
    <p class="hint">A record of what was sent and what came back, to hand to a reviewer as proof a test was done.
    <strong>Save evidence</strong> appears next to the response status after a Send; each card in
    <em>Step through…</em> has one, plus <em>Save all as evidence</em>; and each row of a run's results has a
    <em>Save</em> button, plus <em>Save all as evidence</em>. You enter who is saving it (remembered) and free-text
    notes.</p>
    <dl class="help-dl">
      <dt>What's in it</dt><dd>For each request: the method and resolved URL, the headers as actually sent
        (after auth and the cookie jar were applied), the body, then the response status, headers and body, the
        assertion results and a pass/fail verdict. When the response carries a CPI message-processing-log id
        (<code>SAP_MessageProcessingLogID</code>) it is shown, so the call can be found in the tenant's monitor.</dd>
      <dt>Credentials</dt><dd>Redacted by default, before anything is written to disk: by header (Authorization,
        Cookie, Set-Cookie, CSRF tokens), by field name in JSON, form, XML and query strings (password, secret,
        token&hellip;), and by the value of any secret-named variable wherever it appears. Each is replaced by a
        short fingerprint, and the same value always gets the same one, so a reviewer can see the CSRF token from
        one step is the one sent in the next without seeing it. Cookie <em>names</em> stay visible. Ticking
        <em>Include credentials</em> saves them as-is and marks the pack so.</dd>
      <dt>Formats</dt><dd>A light HTML page (an index at the top links to each request; plain text blocks you can
        select and copy) and plain text that pastes straight into an email or ticket. Download either, or use
        <em>Copy</em> in the list.</dd>
      <dt>The list</dt><dd>The icon at the top of the sidebar (a page with lines) lists every saved pack, newest
        first, with Open, Text, Copy and Delete. Packs are files in the <code>evidence</code> folder of the data
        directory.</dd>
    </dl>
    <p class="hint">Redaction is a safety net, not a guarantee: read a pack before sending it outside your team,
    especially free-text notes and response bodies.</p>

    <div id="help-cookies" class="help-eyebrow">Executing</div>
    <h4>Cookie jar</h4>
    <p class="hint">Cookies a response sets are captured and replayed on later requests automatically, matched
    by domain (exact or subdomain) and path, the way a browser would. The cookie jar (icon next to Help) lists
    everything currently stored, lets you delete one or clear all, or add one by hand — useful for seeding a
    session value you obtained some other way rather than only ever accumulating them from responses.</p>

    <div id="help-settings" class="help-eyebrow">Configuration</div>
    <h4>Settings &amp; network</h4>
    <p class="hint">Global, machine-wide config (the gear icon): a client cert/key pair for mutual TLS (a per-
    environment override is available for when one target needs a different identity), an extra CA bundle for
    internal/self-signed chains, an HTTP(S) proxy URL, and a skip-TLS-verification override for the "I know this
    cert is bad, let me through anyway" case — flagged with a persistent warning dot on the gear icon while it's
    on, so it's hard to leave enabled by accident.</p>

    <div id="help-palette" class="help-eyebrow">Configuration</div>
    <h4>Command palette (⌘K)</h4>
    <p class="hint">Searches everything — every request across every collection (including inside collapsed
    folders, unlike the sidebar filter box), plus actions like switching environments — and jumps straight to
    it. The sidebar filter box only searches the currently open collection's visible tree; ⌘K is for "I know
    what I'm looking for, I don't know where it is."</p>

    <div id="help-examples" class="help-eyebrow">Configuration</div>
    <h4>Example collections</h4>
    <p class="hint">Kept in a separate GitHub repo (not bundled into hapidays itself) so new examples can show up
    without a new release: <a href="https://github.com/achgithub/hapidays-examples" target="_blank" rel="noopener">achgithub/hapidays-examples</a>.
    Use <strong>New → Import from URL…</strong> with a raw file link from that repo, or click below to grab the
    smoke-test collection directly — it exercises most of the auth types and body modes above against public
    test endpoints, a working example of each rather than just a description of it.</p>
    <button id="helpImportSmokeTest">Import the smoke-test collection</button>

    <div class="modal-actions">
      <button id="helpClose" style="background:var(--accent);color:#fff">Close</button>
    </div>
  `);
  $('#helpClose').onclick = closeModal;
  $('#helpImportSmokeTest').onclick = async () => {
    try {
      await api('/collections/import-url', { method: 'POST', body: JSON.stringify({ url: SMOKE_TEST_URL }) });
      await loadCollections();
      closeModal();
    } catch (e) {
      alert('Import failed: ' + e.message);
    }
  };
}

// ---------- cURL import/export ----------

function openCurlImportModal() {
  if (!state.currentCollection) {
    alert('Select or create a collection first — the imported request needs somewhere to go.');
    return;
  }
  showModal(`
    <h3>Import from cURL</h3>
    <p class="hint">Paste a curl command (e.g. "Copy as cURL" from browser devtools). Creates a new request
    in "${escapeAttr(state.currentCollection.name)}".</p>
    <textarea id="curlImportInput" rows="10" placeholder="curl 'https://api.example.com/...' -H 'Authorization: Bearer ...'" style="width:100%;font-family:ui-monospace,monospace"></textarea>
    <div class="modal-actions">
      <button id="curlImportCancel">Cancel</button>
      <button id="curlImportGo" style="background:var(--accent);color:#fff">Import</button>
    </div>
  `);
  $('#curlImportCancel').onclick = closeModal;
  $('#curlImportGo').onclick = async () => {
    const curl = $('#curlImportInput').value.trim();
    if (!curl) return;
    try {
      const spec = await api('/curl/import', { method: 'POST', body: JSON.stringify({ curl }) });
      const name = prompt('Request name:', spec.method + ' ' + (spec.urlRaw || 'request')) || 'Imported request';
      const newIndex = state.currentCollection.root.length;
      state.currentCollection.root.push({ id: crypto.randomUUID(), name, request: spec });
      await persistCollectionTree();
      selectRequest([newIndex]);
      closeModal();
    } catch (e) {
      alert('Could not parse that as a curl command: ' + e.message);
    }
  };
}

async function openCurlExportModal() {
  collectFormIntoRequest();
  let curl;
  try {
    curl = (await api('/curl/export', {
      method: 'POST',
      body: JSON.stringify({
        request: currentRequest,
        collectionId: state.currentCollection ? state.currentCollection.id : '',
        environmentId: state.currentEnvironmentId || '',
      }),
    })).curl;
  } catch (e) {
    alert('Could not generate curl: ' + e.message);
    return;
  }
  showModal(`
    <h3>Copy as cURL</h3>
    <textarea id="curlExportOutput" rows="12" readonly style="width:100%;font-family:ui-monospace,monospace">${escapeAttr(curl)}</textarea>
    <div class="modal-actions">
      <button id="curlExportClose">Close</button>
      <button id="curlExportCopy" style="background:var(--accent);color:#fff">Copy to clipboard</button>
    </div>
  `);
  $('#curlExportClose').onclick = closeModal;
  $('#curlExportCopy').onclick = async () => {
    try {
      await navigator.clipboard.writeText(curl);
      $('#curlExportCopy').textContent = 'Copied!';
    } catch (_) {
      $('#curlExportOutput').select(); // clipboard API unavailable — select the text so Ctrl/Cmd+C still works
    }
  };
}

// ---------- WSDL import ----------

function openWsdlImportModal() {
  showModal(`
    <h3>Import WSDL</h3>
    <p class="hint">Generates one request per SOAP operation, with the endpoint, SOAPAction, and
    envelope skeleton already filled in.</p>
    <div class="field-row"><label>WSDL URL</label><input type="text" id="wsdlUrlInput" placeholder="http://host/service.asmx?WSDL"></div>
    <p class="hint">...or upload a .wsdl/.xml file instead:</p>
    <input type="file" id="wsdlFileInput" accept=".wsdl,.xml">
    <div class="modal-actions">
      <button id="wsdlImportCancel">Cancel</button>
      <button id="wsdlImportGo" style="background:var(--accent);color:#fff">Import</button>
    </div>
  `);
  $('#wsdlImportCancel').onclick = closeModal;
  $('#wsdlImportGo').onclick = async () => {
    const url = $('#wsdlUrlInput').value.trim();
    const file = $('#wsdlFileInput').files[0];
    if (!url && !file) { alert('Provide a WSDL URL or choose a file to upload.'); return; }
    const payload = { url };
    if (file) payload.raw = await file.text();
    try {
      await api('/wsdl/import', { method: 'POST', body: JSON.stringify(payload) });
      await loadCollections();
      closeModal();
    } catch (e) {
      alert('WSDL import failed: ' + e.message);
    }
  };
}

// ---------- OData import ----------

function openODataImportModal() {
  const auth = { type: 'none', params: {} };
  showModal(`
    <h3>Import OData service</h3>
    <p class="hint">Generates a List, Get-by-key, and $filter example request per entity set, from the
    service's $metadata. Detects OData v2 vs v4 and adjusts $filter functions, key-literal quoting, and
    JSON format handling accordingly.</p>
    <div class="field-row"><label>$metadata URL</label><input type="text" id="odataUrlInput" placeholder="https://host/service/\$metadata"></div>
    <label>Auth (needed if the service gates $metadata — SAP Gateway/CPI usually does)</label>
    <select id="odataAuthType">
      <option value="none">No Auth</option>
      <option value="basic">Basic Auth</option>
      <option value="bearer">Bearer Token</option>
      <option value="apikey">API Key</option>
    </select>
    <div id="odataAuthFields"></div>
    <div class="modal-actions">
      <button id="odataImportCancel">Cancel</button>
      <button id="odataImportGo" style="background:var(--accent);color:#fff">Import</button>
    </div>
  `);
  const renderFields = () => populateAuthFields(auth.type, auth.params, $('#odataAuthFields'), renderFields);
  renderFields();
  $('#odataAuthType').onchange = (e) => { auth.type = e.target.value; renderFields(); };
  $('#odataImportCancel').onclick = closeModal;
  $('#odataImportGo').onclick = async () => {
    const url = $('#odataUrlInput').value.trim();
    if (!url) { alert('Provide the service\'s $metadata URL.'); return; }
    try {
      await api('/odata/import', { method: 'POST', body: JSON.stringify({ url, auth }) });
      await loadCollections();
      closeModal();
    } catch (e) {
      alert('OData import failed: ' + e.message);
    }
  };
}

// ---------- OpenAPI / Swagger import ----------

function openOpenAPIImportModal() {
  const auth = { type: 'none', params: {} };
  showModal(`
    <h3>Import OpenAPI / Swagger</h3>
    <p class="hint">Accepts OpenAPI 3.0/3.1 or Swagger 2.0, JSON or YAML. Generates one request per operation,
    grouped into folders by tag, with example values filled in from the spec's schemas — and, since a spec
    describes its own auth, a matching Environment per server it declares (with placeholder credential values;
    see the Help panel's "Collections vs. environments" section for why those aren't collection variables).</p>
    <div class="field-row"><label>Spec URL</label><input type="text" id="openapiUrlInput" placeholder="https://host/openapi.json"></div>
    <label>Auth (only needed if the service gates the spec document itself)</label>
    <select id="openapiAuthType">
      <option value="none">No Auth</option>
      <option value="basic">Basic Auth</option>
      <option value="bearer">Bearer Token</option>
      <option value="apikey">API Key</option>
    </select>
    <div id="openapiAuthFields"></div>
    <p class="hint">...or upload a spec file instead:</p>
    <input type="file" id="openapiFileInput" accept=".json,.yaml,.yml">
    <div class="modal-actions">
      <button id="openapiImportCancel">Cancel</button>
      <button id="openapiImportGo" style="background:var(--accent);color:#fff">Import</button>
    </div>
  `);
  const renderFields = () => populateAuthFields(auth.type, auth.params, $('#openapiAuthFields'), renderFields);
  renderFields();
  $('#openapiAuthType').onchange = (e) => { auth.type = e.target.value; renderFields(); };
  $('#openapiImportCancel').onclick = closeModal;
  $('#openapiImportGo').onclick = async () => {
    const url = $('#openapiUrlInput').value.trim();
    const file = $('#openapiFileInput').files[0];
    if (!url && !file) { alert('Provide a spec URL or choose a file to upload.'); return; }
    const payload = { url, auth };
    if (file) payload.raw = await file.text();
    try {
      const result = await api('/openapi/import', { method: 'POST', body: JSON.stringify(payload) });
      await loadCollections();
      await loadEnvironments();
      closeModal();
      const envCount = (result.environments || []).length;
      if (envCount > 0) {
        alert(`Imported "${result.collection.name}" and ${envCount} environment${envCount === 1 ? '' : 's'} — fill in the placeholder credential values before sending.`);
      }
    } catch (e) {
      alert('OpenAPI import failed: ' + e.message);
    }
  };
}

// ---------- GraphQL introspection import ----------

function openGraphQLImportModal() {
  const auth = { type: 'none', params: {} };
  showModal(`
    <h3>Import GraphQL API</h3>
    <p class="hint">Runs the standard introspection query against the endpoint and generates a Queries folder
    (and a Mutations folder, if the schema has one) with one request per field — arguments and a one-level
    selection set already filled in with placeholders.</p>
    <div class="field-row"><label>GraphQL endpoint URL</label><input type="text" id="graphqlUrlInput" placeholder="https://api.example.com/graphql"></div>
    <label>Auth (needed if the endpoint requires it for introspection too)</label>
    <select id="graphqlAuthType">
      <option value="none">No Auth</option>
      <option value="basic">Basic Auth</option>
      <option value="bearer">Bearer Token</option>
      <option value="apikey">API Key</option>
    </select>
    <div id="graphqlAuthFields"></div>
    <div class="modal-actions">
      <button id="graphqlImportCancel">Cancel</button>
      <button id="graphqlImportGo" style="background:var(--accent);color:#fff">Import</button>
    </div>
  `);
  const renderFields = () => populateAuthFields(auth.type, auth.params, $('#graphqlAuthFields'), renderFields);
  renderFields();
  $('#graphqlAuthType').onchange = (e) => { auth.type = e.target.value; renderFields(); };
  $('#graphqlImportCancel').onclick = closeModal;
  $('#graphqlImportGo').onclick = async () => {
    const url = $('#graphqlUrlInput').value.trim();
    if (!url) { alert('Provide the GraphQL endpoint URL.'); return; }
    try {
      await api('/graphql/import', { method: 'POST', body: JSON.stringify({ url, auth }) });
      await loadCollections();
      closeModal();
    } catch (e) {
      alert('GraphQL import failed: ' + e.message);
    }
  };
}

// ---------- gRPC reflection import ----------

function openGRPCImportModal() {
  showModal(`
    <h3>Import gRPC service</h3>
    <p class="hint">Connects to the target and lists services/methods via server reflection — one request per
    unary method, request JSON pre-filled with a field skeleton. Streaming methods are listed but not imported
    (hapidays only sends unary calls).</p>
    <div class="field-row"><label>Target (host:port)</label><input type="text" id="grpcImportTarget" placeholder="localhost:50051"></div>
    <div class="field-row"><label><input type="checkbox" id="grpcImportPlaintext"> Plaintext (h2c, no TLS)</label></div>
    <div class="modal-actions">
      <button id="grpcImportCancel">Cancel</button>
      <button id="grpcImportGo" style="background:var(--accent);color:#fff">Import</button>
    </div>
  `);
  $('#grpcImportCancel').onclick = closeModal;
  $('#grpcImportGo').onclick = async () => {
    const target = $('#grpcImportTarget').value.trim();
    if (!target) { alert('Provide the gRPC target (host:port).'); return; }
    const plaintext = $('#grpcImportPlaintext').checked;
    try {
      const res = await api('/grpc/import', { method: 'POST', body: JSON.stringify({ target, plaintext }) });
      await loadCollections();
      closeModal();
      if (res.skipped && res.skipped.length) {
        alert('Imported. Skipped ' + res.skipped.length + ' streaming method(s) — hapidays only supports unary gRPC calls:\n'
          + res.skipped.map(s => `${s.fullMethod} (${s.reason})`).join('\n'));
      }
    } catch (e) {
      alert('gRPC import failed: ' + e.message);
    }
  };
}

// ---------- XML pretty-printing ----------

// detectSoapFault finds a SOAP Fault regardless of HTTP status — a fault is
// a valid, well-formed SOAP response and servers commonly return it with
// HTTP 200, so success/failure can't be inferred from status code alone.
// Namespace-agnostic: matches whatever prefix the server used (soap:,
// soapenv:, SOAP-ENV:, s:, or none) for both SOAP 1.1 (faultcode/faultstring)
// and 1.2 (Code/Reason) shapes.
function detectSoapFault(body) {
  if (!body || !/<[\w:.-]*Fault[\s>]/i.test(body)) return null;
  const pick = (re) => (body.match(re) || [, ''])[1].trim();
  const faultstring = pick(/<[\w:.-]*faultstring[^>]*>([\s\S]*?)<\/[\w:.-]*faultstring>/i);
  const faultcode = pick(/<[\w:.-]*faultcode[^>]*>([\s\S]*?)<\/[\w:.-]*faultcode>/i);
  const reason = pick(/<[\w:.-]*Reason[^>]*>[\s\S]*?<[\w:.-]*Text[^>]*>([\s\S]*?)<\/[\w:.-]*Text>/i);
  const code = pick(/<[\w:.-]*Code[^>]*>[\s\S]*?<[\w:.-]*Value[^>]*>([\s\S]*?)<\/[\w:.-]*Value>/i);
  const message = faultstring || reason || '(SOAP Fault — no faultstring/Reason found)';
  const label = faultcode || code || '';
  return { message, label };
}

function looksLikeXml(body, headers) {
  for (const [k, values] of Object.entries(headers || {})) {
    if (k.toLowerCase() === 'content-type' && /xml/i.test(values.join(','))) return true;
  }
  return /^\s*</.test(body);
}

// Re-indents an XML (or SOAP/XHTML) document. Tokenizes rather than naively
// splitting on "><" so a leaf element with inline text (e.g. <Pid>Partner-2
// </Pid>, the common case in the SAP/OData collections this app targets)
// gets printed on one line instead of throwing indentation off for every
// sibling after it.
function formatXml(xml) {
  const tab = '  ';
  const tokenRe = /<!--[\s\S]*?-->|<\?[\s\S]*?\?>|<!\[CDATA\[[\s\S]*?\]\]>|<\/?[^>]+>|[^<]+/g;
  const tokens = xml.match(tokenRe) || [];
  const lines = [];
  let indent = 0;
  const indentStr = () => tab.repeat(indent);
  const nameOf = (tag) => (tag.match(/^<\/?([\w:.-]+)/) || [, ''])[1];

  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i];
    if (!token.startsWith('<') && token.trim() === '') continue; // whitespace between tags

    if (/^<\//.test(token)) {
      indent = Math.max(0, indent - 1);
      lines.push(indentStr() + token);
    } else if (/^<!--/.test(token) || /^<\?/.test(token) || /^<!\[CDATA\[/.test(token) || /\/>$/.test(token)) {
      lines.push(indentStr() + token);
    } else if (token.startsWith('<')) {
      const next = tokens[i + 1];
      const afterNext = tokens[i + 2];
      if (next && !next.startsWith('<') && next.trim() !== '' && afterNext && /^<\//.test(afterNext) && nameOf(afterNext) === nameOf(token)) {
        lines.push(indentStr() + token + next.trim() + afterNext);
        i += 2;
      } else {
        lines.push(indentStr() + token);
        indent++;
      }
    }
  }
  return lines.join('\n');
}

// ---------- misc ----------

function escapeHtml(s) {
  return (s || '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function escapeAttr(s) { return escapeHtml(s); }

// ---------- command palette ----------
//
// Fetches every collection's full tree (state.collections only holds
// summaries) so ⌘K can jump to a request regardless of which collection is
// currently open or which folders are expanded — the gap the sidebar's
// tree filter deliberately leaves. Rebuilt fresh on every open: cheap for
// the collection counts this app deals with, and avoids the index going
// stale after an import/rename/delete.
async function buildPaletteIndex() {
  const cols = await Promise.all(state.collections.map(c => api(`/collections/${c.id}`).catch(() => null)));
  const items = [];
  cols.forEach(col => {
    if (!col) return;
    const walk = (nodes, path, crumbNames, crumbIds) => {
      nodes.forEach((n, i) => {
        const nodePath = path.concat(i);
        if (n.children) {
          walk(n.children, nodePath, crumbNames.concat(n.name), crumbIds.concat(n.id));
        } else if (n.request) {
          items.push({
            type: 'request',
            label: n.name,
            sub: crumbNames.join(' / '),
            method: n.request.protocol === 'odata' ? 'OData'
              : n.request.body && n.request.body.mode === 'graphql' ? 'GQL'
              : n.request.body && n.request.body.mode === 'soap' ? 'SOAP'
              : n.request.method,
            collectionId: col.id,
            nodePath,
            ancestorFolderIds: crumbIds,
          });
        }
      });
    };
    walk(col.root, [], [col.name], []);
  });
  state.environments.forEach(env => items.push({ type: 'env', label: env.name, sub: 'Switch environment', envId: env.id }));
  items.push({ type: 'action', label: 'New collection', sub: 'Action', run: () => $('#newCollectionBtn').click() });
  items.push({ type: 'action', label: 'Cookie jar', sub: 'Action', run: openCookiesModal });
  items.push({ type: 'action', label: 'Settings', sub: 'Action', run: openSettings });
  return items;
}

let paletteItems = [];
let paletteHighlight = 0;

function renderPaletteList(items) {
  const list = $('#paletteList');
  list.innerHTML = '';
  if (items.length === 0) {
    list.innerHTML = '<div class="palette-empty">No matches</div>';
    return;
  }
  items.slice(0, 40).forEach((item, i) => {
    const row = document.createElement('div');
    row.className = 'palette-item' + (i === paletteHighlight ? ' hi' : '');
    const tag = item.type === 'request'
      ? `<span class="method-tag">${escapeHtml(item.method || 'GET')}</span>`
      : `<span class="proto-dot"></span>`;
    row.innerHTML = `${tag}<span class="rname">${escapeHtml(item.label)}</span><span class="rpath">${escapeHtml(item.sub || '')}</span>`;
    row.onclick = () => openPaletteItem(item);
    list.appendChild(row);
  });
}

async function openPaletteItem(item) {
  if (item.type === 'request') {
    if (!state.currentCollection || state.currentCollection.id !== item.collectionId) {
      state.currentCollection = await api(`/collections/${item.collectionId}`);
    }
    item.ancestorFolderIds.forEach(id => state.expandedFolders.add(id));
    selectRequest(item.nodePath);
  } else if (item.type === 'env') {
    state.currentEnvironmentId = item.envId;
    $('#envSelect').value = item.envId;
  } else if (item.type === 'action') {
    item.run();
  }
  closePalette();
}

function filterPaletteItems(query) {
  const q = query.trim().toLowerCase();
  if (!q) return paletteItems;
  return paletteItems.filter(it => (it.label + ' ' + (it.sub || '')).toLowerCase().includes(q));
}

async function openPalette() {
  $('#paletteOverlay').classList.remove('hidden');
  $('#paletteInput').value = '';
  $('#paletteInput').focus();
  paletteHighlight = 0;
  renderPaletteList([]);
  paletteItems = await buildPaletteIndex();
  renderPaletteList(filterPaletteItems(''));
}
function closePalette() {
  $('#paletteOverlay').classList.add('hidden');
}
function togglePalette() {
  $('#paletteOverlay').classList.contains('hidden') ? openPalette() : closePalette();
}

// ---------- response panel resizing ----------

function wireResponseSplitter() {
  const splitter = $('#responseSplitter');
  const responseArea = $('.response-area');
  let startY = 0, startHeight = 0, dragging = false;
  splitter.addEventListener('mousedown', (e) => {
    dragging = true;
    startY = e.clientY;
    startHeight = responseArea.getBoundingClientRect().height;
    document.body.style.cursor = 'row-resize';
    e.preventDefault();
  });
  window.addEventListener('mousemove', (e) => {
    if (!dragging) return;
    const next = Math.max(120, startHeight - (e.clientY - startY));
    responseArea.style.flex = `0 0 ${next}px`;
  });
  window.addEventListener('mouseup', () => {
    if (!dragging) return;
    dragging = false;
    document.body.style.cursor = '';
  });
}

// ---------- wiring ----------

// Catches any button handler that forgot its own try/catch around an
// await api(...) call — without this, a rejected promise in an onclick
// handler fails completely silently and just looks like "nothing happened".
window.addEventListener('unhandledrejection', (e) => {
  alert('Unexpected error: ' + (e.reason && e.reason.message ? e.reason.message : e.reason));
});

document.addEventListener('DOMContentLoaded', () => {
  $('#themeToggleBtn').onclick = () => {
    const next = effectiveTheme() === 'dark' ? 'light' : 'dark';
    try { localStorage.setItem(THEME_KEY, next); } catch (_) { /* ignore — theme just won't persist */ }
    applyTheme(next);
  };
  $('#refreshBtn').onclick = refreshInPlace;
  $('#evidenceBtn').onclick = openEvidenceListModal;
  $('#saveEvidenceBtn').onclick = () => { if (lastEvidenceSource) openSaveEvidenceModal([lastEvidenceSource]); };
  $('#sendBtn').onclick = sendRequest;
  $('#saveRequestBtn').onclick = saveCurrentRequest;
  $('#protocolSelect').onchange = composeUrlFromFields;
  $('#domainInput').oninput = composeUrlFromFields;
  $('#portInput').oninput = composeUrlFromFields;
  $('#pathInput').oninput = composeUrlFromFields;

  $('#importCollectionBtn').onclick = () => $('#importCollectionInput').click();
  $('#importCollectionInput').onchange = (e) => importFile(e.target, '/collections/import', loadCollections);
  $('#importWsdlBtn').onclick = openWsdlImportModal;
  $('#importODataBtn').onclick = openODataImportModal;
  $('#importOpenAPIBtn').onclick = openOpenAPIImportModal;
  $('#importGraphQLBtn').onclick = openGraphQLImportModal;
  $('#importGRPCBtn').onclick = openGRPCImportModal;
  $('#importCurlBtn').onclick = openCurlImportModal;
  $('#copyAsCurlBtn').onclick = openCurlExportModal;
  $('#importCollectionUrlBtn').onclick = () => openImportURLModal('/collections/import-url', loadCollections, {
    title: 'Import collection from URL',
  });

  $('#importEnvBtn').onclick = () => $('#importEnvInput').click();
  $('#importEnvInput').onchange = (e) => importFile(e.target, '/environments/import', loadEnvironments);
  $('#importEnvUrlBtn').onclick = () => openImportURLModal('/environments/import-url', loadEnvironments, {
    title: 'Import environment from URL',
  });

  $('#helpBtn').onclick = openHelpModal;

  $('#editEnvBtn').onclick = openEnvEditor;
  $('#exportEnvBtn').onclick = () => {
    if (!state.currentEnvironmentId) { alert('Select an environment first.'); return; }
    const env = state.environments.find(e => e.id === state.currentEnvironmentId);
    exportEnvironment(state.currentEnvironmentId, env && env.name);
  };
  $('#settingsBtn').onclick = openSettings;
  $('#cookiesBtn').onclick = openCookiesModal;
  $('#suggestCapturesBtn').onclick = runSuggestCaptures;
  $('#newCollectionBtn').onclick = async () => {
    const name = prompt('New collection name:');
    if (!name) return;
    try {
      const id = crypto.randomUUID();
      await api(`/collections/${id}`, { method: 'PUT', body: JSON.stringify({ id, name, root: [] }) });
      await loadCollections();
      await openCollection(id); // select it so it's visibly expanded, not just appended off-screen
    } catch (e) {
      alert('Failed to create collection: ' + e.message);
    }
  };

  $('#envSelect').onchange = (e) => { state.currentEnvironmentId = e.target.value; saveUiState(); };

  $$('#requestTabs .tab').forEach(tab => {
    tab.onclick = () => {
      $$('#requestTabs .tab').forEach(t => { t.classList.remove('active'); t.setAttribute('aria-selected', 'false'); });
      $$('.tab-panel').forEach(p => p.classList.add('hidden'));
      tab.classList.add('active');
      tab.setAttribute('aria-selected', 'true');
      $('#panel-' + tab.dataset.tab).classList.remove('hidden');
    };
  });
  $$('#responseTabs .tab').forEach(tab => {
    tab.onclick = () => {
      $$('#responseTabs .tab').forEach(t => { t.classList.remove('active'); t.setAttribute('aria-selected', 'false'); });
      tab.classList.add('active');
      tab.setAttribute('aria-selected', 'true');
      $('#responseBody').classList.toggle('hidden', tab.dataset.rtab !== 'body');
      $('#responseHeaders').classList.toggle('hidden', tab.dataset.rtab !== 'headers');
    };
  });
  $$('.add-row').forEach(btn => {
    btn.onclick = () => {
      const target = btn.dataset.target;
      if (target === 'params') { currentRequest.query.push({ key: '', value: '', disabled: false }); renderKVTable('paramsTable', currentRequest.query, refreshFullUrlPreview); }
      if (target === 'headers') { currentRequest.headers.push({ key: '', value: '', disabled: false }); renderKVTable('headersTable', currentRequest.headers); }
      if (target === 'captures') { currentRequest.captures.push({ source: 'header', from: '', intoVar: '' }); renderCapturesTable(); }
      if (target === 'assertions') { currentRequest.assertions.push({ type: 'status_equals', target: '', expected: '200', disabled: false }); renderAssertionsTable(); }
      if (target === 'urlencoded') {
        const list = currentRequest.body.urlEncoded || (currentRequest.body.urlEncoded = []);
        list.push({ key: '', value: '', disabled: false });
        renderKVTable('bodyUrlEncodedTable', list);
      }
      if (target === 'formdata') {
        const list = currentRequest.body.formData || (currentRequest.body.formData = []);
        list.push({ key: '', value: '', type: 'text', disabled: false });
        renderFormDataTable();
      }
      if (target === 'grpcMetadata') {
        const grpc = currentRequest.body.grpc || (currentRequest.body.grpc = { target: '', plaintext: false, fullMethod: '', metadata: [] });
        const list = grpc.metadata || (grpc.metadata = []);
        list.push({ key: '', value: '', disabled: false });
        renderKVTable('grpcMetadataTable', list);
      }
    };
  });
  $('#authType').onchange = (e) => { currentRequest.auth.type = e.target.value; renderAuthFields(); };
  $('#bodyMode').onchange = (e) => { currentRequest.body.mode = e.target.value; renderBodyFields(); };
  $('#bodyLanguage').onchange = () => { updatePrettifyVisibility(); updateBodyRawHighlight(); };
  $('#bodyPrettify').onclick = () => {
    const lang = $('#bodyLanguage').value;
    try {
      if (lang === 'json') {
        $('#bodyRaw').value = JSON.stringify(JSON.parse($('#bodyRaw').value), null, 2);
      } else if (lang === 'xml') {
        $('#bodyRaw').value = formatXml($('#bodyRaw').value);
      }
    } catch (_) { /* leave as-is if it doesn't parse */ }
    updateBodyRawHighlight();
  };
  $('#soapVersion').onchange = () => {
    // Switching version swaps which template "Insert envelope" offers, but
    // don't clobber a body someone already wrote.
  };
  $('#wsSecurityMode').onchange = () => {
    $('#wsSecurityCreds').classList.toggle('hidden', !$('#wsSecurityMode').value);
  };
  $('#soapInsertEnvelope').onclick = () => {
    if ($('#bodyRaw').value.trim() && !confirm('Replace the current body with a blank SOAP envelope template?')) return;
    $('#bodyRaw').value = SOAP_ENVELOPE_TEMPLATES[$('#soapVersion').value] || SOAP_ENVELOPE_TEMPLATES['1.1'];
    checkSoapWellFormed();
    updateBodyRawHighlight();
  };
  $('#soapFormatXml').onclick = () => {
    try {
      $('#bodyRaw').value = formatXml($('#bodyRaw').value);
    } catch (_) { /* leave as-is if it doesn't parse */ }
    checkSoapWellFormed();
    updateBodyRawHighlight();
  };
  $('#bodyRaw').addEventListener('input', () => {
    if (currentRequest.body.mode === 'soap') checkSoapWellFormed();
    updateBodyRawHighlight();
  });
  // Scrolling (arrow keys, page down, mouse wheel) doesn't fire 'input' —
  // the highlight pre has its own independent scroll position and drifts
  // out of alignment with the textarea underneath it unless kept in sync.
  $('#bodyRaw').addEventListener('scroll', () => {
    $('#bodyRawHighlight').scrollTop = $('#bodyRaw').scrollTop;
    $('#bodyRawHighlight').scrollLeft = $('#bodyRaw').scrollLeft;
  });
  $('#modalOverlay').onclick = (e) => { if (e.target === $('#modalOverlay')) closeModal(); };
  $('#evidenceOverlay').onclick = (e) => { if (e.target === $('#evidenceOverlay')) closeEvidenceModal(); };

  // ---------- protocol switcher ----------
  $$('#protoSwitch .proto-opt').forEach(btn => {
    btn.onclick = () => setProtocol(btn.dataset.p);
  });

  // ---------- New / Import dropdown ----------
  const newBtn = $('#newBtn');
  const newDropdown = $('#newDropdown');
  newBtn.onclick = (e) => {
    e.stopPropagation();
    const opening = newDropdown.classList.contains('hidden');
    newDropdown.classList.toggle('hidden', !opening);
    newBtn.setAttribute('aria-expanded', String(opening));
  };
  // Any click inside the dropdown (an import/new action) closes it — every
  // item either opens a modal or a file picker, so there's nothing left to
  // do in the dropdown itself once clicked.
  newDropdown.addEventListener('click', () => {
    newDropdown.classList.add('hidden');
    newBtn.setAttribute('aria-expanded', 'false');
  });
  document.addEventListener('click', () => {
    newDropdown.classList.add('hidden');
    newBtn.setAttribute('aria-expanded', 'false');
  });
  $('#newReqMenuItem').onclick = () => {
    if (!state.currentCollection) { alert('Open or create a collection first — the new request needs somewhere to go.'); return; }
    addRequestNode(state.currentCollection.root, []);
  };
  $('#newFolderMenuItem').onclick = () => {
    if (!state.currentCollection) { alert('Open or create a collection first — the new folder needs somewhere to go.'); return; }
    addFolderNode(state.currentCollection.root, null);
  };

  // ---------- sidebar: Collections/History tabs + tree filter ----------
  const tabCollections = $('#sidebarTabCollections');
  const tabHistory = $('#sidebarTabHistory');
  tabCollections.onclick = () => {
    tabCollections.classList.add('active'); tabCollections.setAttribute('aria-selected', 'true');
    tabHistory.classList.remove('active'); tabHistory.setAttribute('aria-selected', 'false');
    $('#collectionList').classList.remove('hidden');
    $('#historyList').classList.add('hidden');
  };
  tabHistory.onclick = () => {
    tabHistory.classList.add('active'); tabHistory.setAttribute('aria-selected', 'true');
    tabCollections.classList.remove('active'); tabCollections.setAttribute('aria-selected', 'false');
    $('#historyList').classList.remove('hidden');
    $('#collectionList').classList.add('hidden');
  };
  $('#treeSearchInput').oninput = (e) => { treeFilterQuery = e.target.value; applyTreeFilter(); };

  // ---------- command palette ----------
  $('#paletteOpenBtn').onclick = openPalette;
  $('#paletteOverlay').onclick = (e) => { if (e.target === $('#paletteOverlay')) closePalette(); };
  $('#paletteInput').oninput = (e) => {
    paletteHighlight = 0;
    renderPaletteList(filterPaletteItems(e.target.value));
  };
  $('#paletteInput').onkeydown = (e) => {
    const visible = filterPaletteItems($('#paletteInput').value).slice(0, 40);
    if (e.key === 'ArrowDown') { e.preventDefault(); paletteHighlight = Math.min(paletteHighlight + 1, visible.length - 1); renderPaletteList(visible); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); paletteHighlight = Math.max(paletteHighlight - 1, 0); renderPaletteList(visible); }
    else if (e.key === 'Enter') { e.preventDefault(); if (visible[paletteHighlight]) openPaletteItem(visible[paletteHighlight]); }
    else if (e.key === 'Escape') { closePalette(); }
  };

  // ---------- global keyboard shortcuts ----------
  document.addEventListener('keydown', (e) => {
    const mod = e.metaKey || e.ctrlKey;
    if (mod && e.key.toLowerCase() === 'k') { e.preventDefault(); togglePalette(); return; }
    if (!$('#paletteOverlay').classList.contains('hidden') && e.key === 'Escape') { closePalette(); return; }
    // Escape closes the topmost dialog: the evidence one sits above the main one.
    if (!$('#evidenceOverlay').classList.contains('hidden') && e.key === 'Escape') { closeEvidenceModal(); return; }
    if (!$('#modalOverlay').classList.contains('hidden') && e.key === 'Escape') { closeModal(); return; }
    // Below here, ignore shortcuts fired from within the palette input or a
    // modal — Enter in those has its own meaning already wired above.
    if (document.activeElement === $('#paletteInput')) return;
    if (mod && e.key === 'Enter') { e.preventDefault(); sendRequest(); return; }
    if (mod && e.key.toLowerCase() === 's') { e.preventDefault(); saveCurrentRequest(); return; }
  });

  // ---------- response panel resize + dirty/badge tracking ----------
  wireResponseSplitter();
  const workspace = $('#requestWorkspace');
  ['input', 'change', 'click'].forEach(evt => workspace.addEventListener(evt, onWorkspaceChanged));

  renderRequestForm();
  updateTlsWarnDot();
  const savedUiState = readUiState(); // read before anything renders and could overwrite it
  Promise.all([loadCollections(), loadEnvironments(), loadHistory()])
    .then(() => restoreUiState(savedUiState))
    .finally(() => { uiStateRestored = true; saveUiState(); });
});
