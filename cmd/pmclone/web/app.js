// pmclone frontend. Vanilla JS, no framework/CDN — this has to keep
// working on networks that block everything but this one binary.
'use strict';

const state = {
  collections: [],        // summaries
  currentCollection: null, // full object incl. tree
  environments: [],
  currentEnvironmentId: '',
  selectedPath: null,      // array of indices into currentCollection.root leading to the request node
  expandedFolders: new Set(), // node ids of folders currently expanded in the tree
  settings: {},
};

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

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
  const container = $('#collectionList');
  container.innerHTML = '';
  for (const col of state.collections) {
    const wrap = document.createElement('div');
    wrap.className = 'tree-node';
    const header = document.createElement('div');
    header.className = 'tree-folder';
    header.textContent = col.name || '(untitled collection)';

    const actions = document.createElement('span');
    actions.className = 'col-actions';

    const run = document.createElement('span');
    run.className = 'col-action';
    run.textContent = '▶';
    run.title = 'Run this collection';
    run.onclick = (e) => { e.stopPropagation(); openRunModal(col.id, null, col.name); };
    actions.appendChild(run);

    const auth = document.createElement('span');
    auth.className = 'col-action';
    auth.textContent = '🔑';
    auth.title = 'Collection auth (used by requests set to "Inherit from collection")';
    auth.onclick = (e) => { e.stopPropagation(); openCollectionAuthModal(col.id); };
    actions.appendChild(auth);

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
  renderRequestForm();
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
async function openCollectionAuthModal(collectionId) {
  const col = (state.currentCollection && state.currentCollection.id === collectionId)
    ? state.currentCollection
    : await api(`/collections/${collectionId}`);
  const auth = col.auth && col.auth.type ? JSON.parse(JSON.stringify(col.auth)) : { type: 'none', params: {} };
  if (!auth.params) auth.params = {};

  showModal(`
    <h3>Collection auth — ${escapeHtml(col.name)}</h3>
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
      <button id="colAuthCancel">Cancel</button>
      <button id="colAuthSave" style="background:var(--accent);color:#fff">Save</button>
    </div>
  `);

  const rerender = () => populateAuthFields(auth.type, auth.params, $('#colAuthFields'), rerender);
  $('#colAuthType').value = auth.type;
  rerender();
  $('#colAuthType').onchange = (e) => { auth.type = e.target.value; rerender(); };
  $('#colAuthCancel').onclick = closeModal;
  $('#colAuthSave').onclick = async () => {
    col.auth = auth;
    const saved = await api(`/collections/${col.id}`, { method: 'PUT', body: JSON.stringify(col) });
    if (state.currentCollection && state.currentCollection.id === col.id) state.currentCollection = saved;
    closeModal();
  };
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
      folderDiv.textContent = (expanded ? '📂 ' : '📁 ') + node.name;

      const actions = document.createElement('span');
      actions.className = 'col-actions';
      const run = document.createElement('span');
      run.className = 'col-action';
      run.textContent = '▶';
      run.title = 'Run this folder';
      run.onclick = (e) => { e.stopPropagation(); openRunModal(state.currentCollection.id, node.id, node.name); };
      actions.appendChild(run);
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
      reqDiv.className = `tree-request method-${method.replace(/[^A-Za-z]/g, '')}` + (samePath(nodePath, state.selectedPath) ? ' selected' : '');
      reqDiv.innerHTML = `<span class="method-tag">${escapeHtml(method)}</span>${escapeHtml(node.name)}`;
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

function blankRequest() {
  return {
    method: 'GET', urlRaw: '', query: [], headers: [],
    auth: { type: 'none', params: {} },
    body: { mode: 'none' },
    captures: [], preRequestScript: '', testScript: '', hasScript: false,
  };
}

function loadRequestIntoForm(req) {
  currentRequest = JSON.parse(JSON.stringify(req));
  if (!currentRequest.auth) currentRequest.auth = { type: 'none', params: {} };
  if (!currentRequest.auth.params) currentRequest.auth.params = {};
  if (!currentRequest.body) currentRequest.body = { mode: 'none' };
  renderRequestForm();
}

function renderRequestForm() {
  $('#methodSelect').value = currentRequest.method || 'GET';
  $('#urlInput').value = currentRequest.urlRaw || '';
  renderKVTable('paramsTable', currentRequest.query || (currentRequest.query = []));
  renderKVTable('headersTable', currentRequest.headers || (currentRequest.headers = []));
  renderCapturesTable();

  $('#authType').value = currentRequest.auth.type || 'none';
  renderAuthFields();

  $('#bodyMode').value = currentRequest.body.mode || 'none';
  $('#bodyLanguage').value = currentRequest.body.rawLanguage || 'json';
  $('#bodyRaw').value = currentRequest.body.raw || '';
  renderBodyFields();

  $('#preScriptView').value = currentRequest.preRequestScript || '';
  $('#testScriptView').value = currentRequest.testScript || '';
}

function renderKVTable(containerId, list) {
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
    chk.onchange = () => { kv.disabled = !chk.checked; };
    keyInput.oninput = () => { kv.key = keyInput.value; };
    valInput.oninput = () => { kv.value = valInput.value; };
    row.querySelector('.remove-row').onclick = () => { list.splice(i, 1); renderKVTable(containerId, list); };
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

// Best-effort scan of imported Postman scripts for the handful of
// pm.*.set(...) patterns that map cleanly onto a Capture rule (see the
// table in the app's docs). Anything with conditionals, loops, or string
// manipulation isn't recognized — those have no Capture equivalent and
// still need a human to read the script.
function suggestCapturesFromScript() {
  const script = (currentRequest.testScript || '') + '\n' + (currentRequest.preRequestScript || '');
  const SETTER = String.raw`pm\.(?:environment|collectionVariables|globals)\.set`;
  const suggestions = [];

  // Inline forms: pm.environment.set("x", pm.response.headers.get("H")) / .json().a.b
  const headerDirectRe = new RegExp(SETTER + String.raw`\(\s*["']([^"']+)["']\s*,\s*pm\.response\.headers\.get\(\s*["']([^"']+)["']\s*\)\s*\)`, 'g');
  for (const m of script.matchAll(headerDirectRe)) suggestions.push({ source: 'header', from: m[2], intoVar: m[1] });

  const jsonDirectRe = new RegExp(SETTER + String.raw`\(\s*["']([^"']+)["']\s*,\s*pm\.response\.json\(\)\.([\w.]+)\s*\)`, 'g');
  for (const m of script.matchAll(jsonDirectRe)) suggestions.push({ source: 'body_json', from: m[2], intoVar: m[1] });

  // Via-intermediate-variable forms — the common case in practice (e.g. the
  // stock SAP/OData "X-CSRF-Token: Fetch" script): a var/let/const captures
  // the header or json() call, and a later pm.*.set(...) statement uses it.
  const assignRe = /(?:var|let|const)\s+(\w+)\s*=\s*pm\.response\.(headers\.get\(\s*["']([^"']+)["']\s*\)|json\(\))/g;
  for (const am of script.matchAll(assignRe)) {
    const varName = am[1];
    if (am[2].startsWith('headers')) {
      const headerName = am[3];
      const useRe = new RegExp(SETTER + String.raw`\(\s*["']([^"']+)["']\s*,\s*` + varName + String.raw`\s*\)`, 'g');
      for (const m of script.matchAll(useRe)) suggestions.push({ source: 'header', from: headerName, intoVar: m[1] });
    } else {
      const useRe = new RegExp(SETTER + String.raw`\(\s*["']([^"']+)["']\s*,\s*` + varName + String.raw`\.([\w.]+)\s*\)`, 'g');
      for (const m of script.matchAll(useRe)) suggestions.push({ source: 'body_json', from: m[2], intoVar: m[1] });
    }
  }
  return suggestions;
}

function runSuggestCaptures() {
  const suggestions = suggestCapturesFromScript();
  const existing = currentRequest.captures || (currentRequest.captures = []);
  let added = 0;
  for (const s of suggestions) {
    const dupe = existing.some(c => c.source === s.source && c.from === s.from && c.intoVar === s.intoVar);
    if (!dupe) { existing.push(s); added++; }
  }
  renderCapturesTable();
  if (suggestions.length === 0) {
    alert('No recognizable pm.environment.set(...) patterns found in the scripts above — this script likely needs manual conversion (conditionals, loops, or chained requests aren\'t supported by Capture rules).');
  } else if (added === 0) {
    alert('Found the same pattern(s) already present as capture rules — nothing new to add.');
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
      params.accessToken = await fetchOAuth2Token(params, grantSelect.value);
      tokenPreview.value = params.accessToken;
    } catch (e) {
      alert('OAuth2 token fetch failed: ' + e.message);
    } finally {
      getTokenBtn.disabled = false;
      getTokenBtn.textContent = 'Get New Access Token';
    }
  };
  tokenRow.appendChild(tokenPreview);
  tokenRow.appendChild(getTokenBtn);
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
    const result = await api('/oauth2/authorize/wait', { method: 'POST', body: JSON.stringify({ sessionId: start.sessionId }) });
    return result.accessToken;
  }
  const result = await api('/oauth2/token', {
    method: 'POST',
    body: JSON.stringify({
      grantType, accessTokenUrl: params.accessTokenUrl,
      clientId: params.clientId, clientSecret: params.clientSecret,
      username: params.username, password: params.password, scope: params.scope,
    }),
  });
  return result.accessToken;
}

function renderBodyFields() {
  const mode = currentRequest.body.mode;
  $('#bodyRaw').classList.toggle('hidden', mode !== 'raw');
  $('#bodyLanguage').classList.toggle('hidden', mode !== 'raw');
  $('#bodyUrlEncodedTable').classList.toggle('hidden', mode !== 'urlencoded');
  if (mode === 'urlencoded') {
    renderKVTable('bodyUrlEncodedTable', currentRequest.body.urlEncoded || (currentRequest.body.urlEncoded = []));
  }
}

function collectFormIntoRequest() {
  currentRequest.method = $('#methodSelect').value;
  currentRequest.urlRaw = $('#urlInput').value;
  currentRequest.body.mode = $('#bodyMode').value;
  currentRequest.body.rawLanguage = $('#bodyLanguage').value;
  currentRequest.body.raw = $('#bodyRaw').value;
  return currentRequest;
}

// ---------- send ----------

async function sendRequest() {
  collectFormIntoRequest();
  $('#responseStatus').textContent = 'Sending…';
  $('#responseStatus').className = 'response-status';
  $('#responseBody').textContent = '';
  try {
    const result = await api('/send', {
      method: 'POST',
      body: JSON.stringify({
        request: currentRequest,
        collectionId: state.currentCollection ? state.currentCollection.id : '',
        environmentId: state.currentEnvironmentId,
      }),
    });
    renderResponse(result);
    if (result.captured && Object.keys(result.captured).length) {
      await loadEnvironments();
    }
    loadHistory();
  } catch (e) {
    $('#responseStatus').textContent = 'Error: ' + e.message;
    $('#responseStatus').className = 'response-status status-err';
  }
}

function renderResponse(result) {
  if (result.error) {
    $('#responseStatus').textContent = `Error: ${result.error} (${result.durationMs}ms)`;
    $('#responseStatus').className = 'response-status status-err';
    $('#responseBody').textContent = '';
    return;
  }
  const statusClass = 'status-' + Math.floor(result.status / 100);
  $('#responseStatus').className = 'response-status ' + statusClass;
  $('#responseStatus').textContent = `${result.status} ${result.statusText || ''} · ${result.durationMs}ms · ${result.sizeBytes}B`;

  let bodyText = result.bodyIsBase64 ? '(binary response, base64)\n' + result.body : result.body;
  if (!result.bodyIsBase64) {
    try {
      bodyText = JSON.stringify(JSON.parse(result.body), null, 2);
    } catch (_) {
      if (looksLikeXml(result.body, result.headers)) bodyText = formatXml(result.body);
    }
  }
  $('#responseBody').textContent = bodyText;

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
  const name = env ? env.name : 'New Environment';

  showModal(`
    <h3>Environment</h3>
    <div class="field-row"><label>Name</label><input type="text" id="envNameInput" value="${escapeAttr(name)}"></div>
    <div id="envValuesTable" class="kv-table"></div>
    <button class="add-row" id="envAddRow">+ Add variable</button>
    <div class="modal-actions">
      <button id="envCancel">Cancel</button>
      <button id="envSave" style="background:var(--accent);color:#fff">Save</button>
    </div>
  `);

  const renderEnvTable = () => renderKVTable('envValuesTable', values);
  renderEnvTable();
  $('#envAddRow').onclick = () => { values.push({ key: '', value: '', disabled: false }); renderEnvTable(); };
  $('#envCancel').onclick = closeModal;
  $('#envSave').onclick = async () => {
    const payload = { id: env ? env.id : '', name: $('#envNameInput').value, values };
    const saved = env
      ? await api(`/environments/${env.id}`, { method: 'PUT', body: JSON.stringify(payload) })
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
      <button id="runStart" style="background:var(--accent);color:#fff">Run</button>
    </div>
    <div id="runResults"></div>
  `);
  $('#runCancel').onclick = closeModal;
  $('#runStart').onclick = async () => {
    $('#runStart').disabled = true;
    $('#runStart').textContent = 'Running…';
    try {
      let dataRows = null;
      const file = $('#runDataFile').files[0];
      if (file) dataRows = await parseDataFile(file);
      const iterations = parseInt($('#runIterations').value, 10) || 1;
      if (!dataRows && iterations > 1) dataRows = Array.from({ length: iterations }, () => ({}));

      const results = await api('/run', {
        method: 'POST',
        body: JSON.stringify({
          collectionId, folderId: folderId || undefined,
          environmentId: state.currentEnvironmentId,
          dataRows, delayMs: parseInt($('#runDelay').value, 10) || 0,
        }),
      });
      renderRunResults(results);
    } catch (e) {
      $('#runResults').textContent = 'Run failed: ' + e.message;
    } finally {
      $('#runStart').disabled = false;
      $('#runStart').textContent = 'Run';
    }
  };
}

function renderRunResults(results) {
  const passCount = results.filter(r => !r.error && r.status >= 200 && r.status < 400).length;
  const container = $('#runResults');
  container.innerHTML = '';
  const summary = document.createElement('p');
  summary.textContent = `${passCount}/${results.length} passed (2xx/3xx, no transport error)`;
  container.appendChild(summary);
  const table = document.createElement('div');
  table.className = 'kv-table';
  results.forEach(r => {
    const row = document.createElement('div');
    row.className = 'kv-row';
    const ok = !r.error && r.status >= 200 && r.status < 400;
    row.innerHTML = `<span style="flex:1;color:${ok ? 'var(--ok)' : 'var(--danger)'}">${escapeHtml(String(r.iteration))} · ${escapeHtml(r.method)} ${escapeHtml(r.error || String(r.status))} · ${r.durationMs}ms · ${escapeHtml(r.name)}</span>`;
    table.appendChild(row);
  });
  container.appendChild(table);
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
      row.innerHTML = `<span style="flex:1">${escapeHtml(c.domain)} — ${escapeHtml(c.name)}=${escapeHtml(c.value)}</span>
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
    <div class="modal-actions">
      <button id="setCancel">Cancel</button>
      <button id="setSave" style="background:var(--accent);color:#fff">Save</button>
    </div>
  `);
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

// ---------- XML pretty-printing ----------

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

// ---------- wiring ----------

// Catches any button handler that forgot its own try/catch around an
// await api(...) call — without this, a rejected promise in an onclick
// handler fails completely silently and just looks like "nothing happened".
window.addEventListener('unhandledrejection', (e) => {
  alert('Unexpected error: ' + (e.reason && e.reason.message ? e.reason.message : e.reason));
});

document.addEventListener('DOMContentLoaded', () => {
  $('#sendBtn').onclick = sendRequest;
  $('#saveRequestBtn').onclick = saveCurrentRequest;

  $('#importCollectionBtn').onclick = () => $('#importCollectionInput').click();
  $('#importCollectionInput').onchange = (e) => importFile(e.target, '/collections/import', loadCollections);

  $('#importEnvBtn').onclick = () => $('#importEnvInput').click();
  $('#importEnvInput').onchange = (e) => importFile(e.target, '/environments/import', loadEnvironments);

  $('#editEnvBtn').onclick = openEnvEditor;
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

  $('#envSelect').onchange = (e) => { state.currentEnvironmentId = e.target.value; };

  $$('#requestTabs .tab').forEach(tab => {
    tab.onclick = () => {
      $$('#requestTabs .tab').forEach(t => t.classList.remove('active'));
      $$('.tab-panel').forEach(p => p.classList.add('hidden'));
      tab.classList.add('active');
      $('#panel-' + tab.dataset.tab).classList.remove('hidden');
    };
  });
  $$('#responseTabs .tab').forEach(tab => {
    tab.onclick = () => {
      $$('#responseTabs .tab').forEach(t => t.classList.remove('active'));
      tab.classList.add('active');
      $('#responseBody').classList.toggle('hidden', tab.dataset.rtab !== 'body');
      $('#responseHeaders').classList.toggle('hidden', tab.dataset.rtab !== 'headers');
    };
  });
  $$('.add-row').forEach(btn => {
    btn.onclick = () => {
      const target = btn.dataset.target;
      if (target === 'params') { currentRequest.query.push({ key: '', value: '', disabled: false }); renderKVTable('paramsTable', currentRequest.query); }
      if (target === 'headers') { currentRequest.headers.push({ key: '', value: '', disabled: false }); renderKVTable('headersTable', currentRequest.headers); }
      if (target === 'captures') { currentRequest.captures.push({ source: 'header', from: '', intoVar: '' }); renderCapturesTable(); }
    };
  });
  $('#authType').onchange = (e) => { currentRequest.auth.type = e.target.value; renderAuthFields(); };
  $('#bodyMode').onchange = (e) => { currentRequest.body.mode = e.target.value; renderBodyFields(); };
  $('#modalOverlay').onclick = (e) => { if (e.target === $('#modalOverlay')) closeModal(); };

  renderRequestForm();
  loadCollections();
  loadEnvironments();
  loadHistory();
});
