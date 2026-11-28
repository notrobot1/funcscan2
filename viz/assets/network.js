// RAW_NODES, RAW_EDGES and LEGEND are defined by the page before this script.

// HTML-escape helper: graph data goes into innerHTML only through this.
function esc(s) {
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

const nodesDS = new vis.DataSet(RAW_NODES.map((n, i) => ({
  id: n.id, label: n.label, color: n.color, size: n.size,
  font: n.font, title: n.title,
  // Spread the nodes out on a golden-angle spiral before the physics runs.
  // Starting them all at one point makes the repulsion between overlapping
  // nodes blow up, which on a large graph overflows the solver's recursion.
  x: 30 * Math.sqrt(i) * Math.cos(i * 2.4),
  y: 30 * Math.sqrt(i) * Math.sin(i * 2.4),
  community: n.community, community_name: n.community_name,
  source_file: n.source_file, file_type: n.file_type, degree: n.degree,
  member_count: n.member_count,
})));

const edgesDS = new vis.DataSet(RAW_EDGES.map((e, i) => ({
  id: i, from: e.from, to: e.to,
  label: '',
  title: e.title,
  dashes: e.dashes,
  width: e.width,
  color: e.color,
  arrows: { to: { enabled: true, scaleFactor: 0.5 } },
})));

const container = document.getElementById('graph');
const network = new vis.Network(container, { nodes: nodesDS, edges: edgesDS }, {
  physics: {
    enabled: true,
    solver: 'forceAtlas2Based',
    forceAtlas2Based: {
      gravitationalConstant: -60,
      centralGravity: 0.005,
      springLength: 120,
      springConstant: 0.08,
      damping: 0.4,
      avoidOverlap: 0.8,
    },
    // The layout is simulated for at most this many steps...
    stabilization: { iterations: 200, fit: true },
  },
  interaction: {
    hover: true,
    tooltipDelay: 100,
    // While the view is dragged only the nodes are redrawn.
    hideEdgesOnDrag: true,
    navigationButtons: false,
    keyboard: false,
  },
  nodes: { shape: 'dot', borderWidth: 1.5 },
  edges: { smooth: { type: 'continuous', roundness: 0.2 }, selectionWidth: 3 },
});

// Theme: light, dark or the system's, remembered between visits.
const THEME_KEY = 'funcscan-theme';
const themeMQ = window.matchMedia ? window.matchMedia('(prefers-color-scheme: light)') : null;
let themeLabelColor = '#ffffff';  // the node font colour the page was generated with
function themePref() {
  let p = null;
  try { p = localStorage.getItem(THEME_KEY); } catch (e) {}
  return (p === 'light' || p === 'dark' || p === 'system') ? p : null;
}
function applyTheme(pref) {
  const t = pref === 'light' ? 'light' : (pref === 'system' && themeMQ && themeMQ.matches) ? 'light' : 'dark';
  const root = document.documentElement;
  if (!root) return;
  root.setAttribute('data-theme', t);
  document.querySelectorAll('[data-theme-choice]').forEach(b => {
    b.setAttribute('aria-pressed', String(b.dataset.themeChoice === (pref || 'dark')));
  });
  const labelColor = getComputedStyle(root).getPropertyValue('--node-label').trim();
  if (labelColor && labelColor !== themeLabelColor) {
    themeLabelColor = labelColor;
    nodesDS.update(nodesDS.get().map(n => ({ id: n.id, font: Object.assign({}, n.font, { color: labelColor }) })));
  }
}
document.querySelectorAll('[data-theme-choice]').forEach(b => {
  b.addEventListener('click', () => {
    const pref = b.dataset.themeChoice;
    try { localStorage.setItem(THEME_KEY, pref); } catch (e) {}
    applyTheme(pref);
  });
});
if (themeMQ) {
  const onSchemeChange = () => { if (themePref() === 'system') applyTheme('system'); };
  if (themeMQ.addEventListener) themeMQ.addEventListener('change', onSchemeChange);
  else if (themeMQ.addListener) themeMQ.addListener(onSchemeChange);
}
applyTheme(themePref());

// ...and then switched off for good: the nodes stay where they settled and the
// page costs no CPU while it is only being looked at.
network.once('stabilizationIterationsDone', () => {
  network.setOptions({ physics: { enabled: false } });
});

function showInfo(nodeId) {
  const n = nodesDS.get(nodeId);
  if (!n) return;
  const neighborIds = network.getConnectedNodes(nodeId);
  const neighborItems = neighborIds.map(nid => {
    const nb = nodesDS.get(nid);
    const color = nb ? nb.color.background : '#555';
    return `<span class="neighbor-link" style="border-left-color:${esc(color)}" data-nid="${esc(nid)}">${esc(nb ? nb.label : nid)}</span>`;
  }).join('');
  const isMetaNode = !n.file_type && !n.source_file;
  let html = `<div class="field"><b>${esc(n.label)}</b></div>`;
  if (!isMetaNode) {
    html += `<div class="field">Type: ${esc(n.file_type || 'unknown')}</div>`;
  }
  html += `<div class="field">Community: ${esc(n.community_name || '')}</div>`;
  if (!isMetaNode) {
    html += `<div class="field">Source: ${esc(n.source_file || '-')}</div>`;
  } else if (n.member_count !== undefined && n.member_count !== null) {
    html += `<div class="field">Members: ${esc(n.member_count)}</div>`;
  }
  html += `<div class="field">Degree: ${n.degree}</div>`;
  if (neighborIds.length) {
    html += `<div class="field" style="margin-top:8px;color:var(--text-2);font-size:11px">Neighbors (${neighborIds.length})</div><div id="neighbors-list">${neighborItems}</div>`;
  }
  document.getElementById('info-content').innerHTML = html;
}

function focusNode(nodeId) {
  network.focus(nodeId, { scale: 1.4, animation: true });
  network.selectNodes([nodeId]);
  showInfo(nodeId);
}

// Neighbour links carry the node id in a data attribute and share one
// listener. An id can hold a quote; written into an inline onclick it would
// break the link, or let a hostile name run script in the page.
document.addEventListener('click', e => {
  const el = e.target.closest('.neighbor-link');
  if (el && el.dataset.nid !== undefined) focusNode(el.dataset.nid);
});

// Tooltips for the sidebar: a name gets one only when it is cut short, a
// count always, to say what it counts. Text is set with textContent only.
const sidebarTip = document.createElement('div');
sidebarTip.className = 'vis-tooltip';
sidebarTip.style.cssText = 'white-space: normal; max-width: 360px; overflow-wrap: break-word;';
document.body.appendChild(sidebarTip);
let sidebarTipTimer = null;
let sidebarTipEl = null;    // element whose text the tooltip shows
let sidebarTipZone = null;  // element the pointer must leave to hide it
function hideSidebarTip() {
  clearTimeout(sidebarTipTimer);
  sidebarTip.style.visibility = 'hidden';
  sidebarTip.textContent = '';
  sidebarTipEl = sidebarTipZone = null;
}
document.addEventListener('mouseover', e => {
  const zone = e.target.closest && e.target.closest('.legend-count, .legend-item, .search-item, .neighbor-link');
  const el = zone && zone.classList.contains('legend-item') ? zone.querySelector('.legend-label') : zone;
  if (!el || el === sidebarTipEl) return;
  hideSidebarTip();
  const isCount = el.classList.contains('legend-count');
  if (!isCount && el.scrollWidth <= el.clientWidth) return;
  const text = isCount ? `${el.textContent} ${el.textContent === '1' ? 'node' : 'nodes'}` : el.textContent;
  sidebarTipEl = el;
  sidebarTipZone = zone;
  const x = e.clientX, y = e.clientY;
  sidebarTipTimer = setTimeout(() => {
    sidebarTip.textContent = text;
    sidebarTip.style.left = sidebarTip.style.top = '0px';
    sidebarTip.style.visibility = 'visible';
    const w = sidebarTip.offsetWidth, h = sidebarTip.offsetHeight;
    sidebarTip.style.left = Math.max(4, Math.min(x + 12, window.innerWidth - w - 4)) + 'px';
    sidebarTip.style.top = (y + 16 + h > window.innerHeight ? Math.max(4, y - h - 8) : y + 16) + 'px';
  }, 100);
});
document.addEventListener('mouseout', e => {
  if (sidebarTipZone && !sidebarTipZone.contains(e.relatedTarget)) hideSidebarTip();
});
document.addEventListener('mousedown', hideSidebarTip);

// Hover is a more reliable way to tell which node a click was on.
let hoveredNodeId = null;
network.on('hoverNode', params => {
  hoveredNodeId = params.node;
  container.style.cursor = 'pointer';
});
network.on('blurNode', () => {
  hoveredNodeId = null;
  container.style.cursor = 'default';
});
container.addEventListener('click', () => {
  if (hoveredNodeId !== null) {
    showInfo(hoveredNodeId);
    network.selectNodes([hoveredNodeId]);
  }
});
network.on('click', params => {
  if (params.nodes.length > 0) {
    showInfo(params.nodes[0]);
  } else if (hoveredNodeId === null) {
    document.getElementById('info-content').innerHTML = '<span class="empty">Click a node to inspect it</span>';
  }
});

const searchInput = document.getElementById('search');
const searchResults = document.getElementById('search-results');
searchInput.addEventListener('input', () => {
  const q = searchInput.value.toLowerCase().trim();
  searchResults.innerHTML = '';
  if (!q) { searchResults.style.display = 'none'; return; }
  const matches = RAW_NODES.filter(n => n.label.toLowerCase().includes(q)).slice(0, 20);
  if (!matches.length) { searchResults.style.display = 'none'; return; }
  searchResults.style.display = 'block';
  matches.forEach(n => {
    const el = document.createElement('div');
    el.className = 'search-item';
    el.textContent = n.label;
    el.style.borderLeft = `3px solid ${n.color.background}`;
    el.style.paddingLeft = '8px';
    el.onclick = () => {
      network.focus(n.id, { scale: 1.5, animation: true });
      network.selectNodes([n.id]);
      showInfo(n.id);
      searchResults.style.display = 'none';
      searchInput.value = '';
    };
    searchResults.appendChild(el);
  });
});
document.addEventListener('click', e => {
  if (!searchResults.contains(e.target) && e.target !== searchInput)
    searchResults.style.display = 'none';
});

const hiddenCommunities = new Set();
const selectAllCb = document.getElementById('select-all-cb');

function updateSelectAllState() {
  const total = LEGEND.length;
  const hidden = hiddenCommunities.size;
  selectAllCb.checked = hidden === 0;
  selectAllCb.indeterminate = hidden > 0 && hidden < total;
}

function toggleAllCommunities(hide) {
  document.querySelectorAll('.legend-item').forEach(item => {
    hide ? item.classList.add('dimmed') : item.classList.remove('dimmed');
  });
  document.querySelectorAll('.legend-cb').forEach(cb => {
    cb.checked = !hide;
  });
  LEGEND.forEach(c => {
    if (hide) hiddenCommunities.add(c.cid); else hiddenCommunities.delete(c.cid);
  });
  nodesDS.update(RAW_NODES.map(n => ({ id: n.id, hidden: hide })));
  updateSelectAllState();
}
selectAllCb.addEventListener('change', () => toggleAllCommunities(!selectAllCb.checked));

const legendEl = document.getElementById('legend');
LEGEND.forEach(c => {
  const item = document.createElement('div');
  item.className = 'legend-item';
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.className = 'legend-cb';
  cb.checked = true;
  cb.addEventListener('change', (e) => {
    e.stopPropagation();
    if (cb.checked) {
      hiddenCommunities.delete(c.cid);
      item.classList.remove('dimmed');
    } else {
      hiddenCommunities.add(c.cid);
      item.classList.add('dimmed');
    }
    nodesDS.update(RAW_NODES
      .filter(n => n.community === c.cid)
      .map(n => ({ id: n.id, hidden: !cb.checked })));
    updateSelectAllState();
  });
  // c.label arrives HTML-escaped.
  item.innerHTML = `<div class="legend-dot" style="background:${c.color}"></div>
    <span class="legend-label">${c.label}</span>
    <span class="legend-count">${c.count}</span>`;
  item.prepend(cb);
  item.onclick = (e) => {
    if (e.target === cb) return;
    cb.checked = !cb.checked;
    cb.dispatchEvent(new Event('change'));
  };
  legendEl.appendChild(item);
});
