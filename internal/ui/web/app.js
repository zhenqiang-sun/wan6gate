'use strict';

const $ = (s) => document.querySelector(s);
const $$ = (s) => document.querySelectorAll(s);

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const r = await fetch(path, opts);
  let j = {};
  try { j = await r.json(); } catch {}
  if (!r.ok) {
    const err = new Error(j.error || `HTTP ${r.status}`);
    err.status = r.status;
    throw err;
  }
  return j.data;
}

function openModal(id) {
  $('#modal-bg').classList.remove('hidden');
  $$('.modal').forEach(m => m.classList.add('hidden'));
  const m = $(`#modal-${id}`);
  if (m) m.classList.remove('hidden');
}
function closeModals() {
  $('#modal-bg').classList.add('hidden');
  $$('.modal').forEach(m => m.classList.add('hidden'));
}
$('#modal-bg').onclick = closeModals;

function showFatal(msg) {
  document.body.innerHTML = `<div style="padding:2em;color:#c0392b;font-family:sans-serif">致命错误：${escape(msg)}</div>`;
}
function escape(s) {
  return String(s ?? '').replace(/[<>&"]/g, c => ({ '<':'&lt;', '>':'&gt;', '&':'&amp;', '"':'&quot;' }[c]));
}

function fmtBytes(n) {
  if (!n || n < 1024) return (n || 0) + ' B';
  const u = ['KB','MB','GB','TB'];
  let i = -1; let v = n;
  do { v /= 1024; i++; } while (v >= 1024 && i < u.length - 1);
  return v.toFixed(v >= 100 ? 0 : (v >= 10 ? 1 : 2)) + ' ' + u[i];
}
function fmtDuration(s) {
  if (s < 60) return s + 's';
  if (s < 3600) return Math.floor(s/60) + 'm' + (s%60).toString().padStart(2,'0') + 's';
  if (s < 86400) return Math.floor(s/3600) + 'h' + Math.floor((s%3600)/60).toString().padStart(2,'0') + 'm';
  return Math.floor(s/86400) + 'd' + Math.floor((s%86400)/3600) + 'h';
}
function fmtRelTime(iso) {
  if (!iso) return '—';
  const t = new Date(iso).getTime();
  if (!t) return '—';
  const sec = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (sec < 60) return sec + ' 秒前';
  if (sec < 3600) return Math.floor(sec/60) + ' 分钟前';
  if (sec < 86400) return Math.floor(sec/3600) + ' 小时前';
  return Math.floor(sec/86400) + ' 天前';
}
function fmtKB(kb) { return fmtBytes(kb * 1024); }

let logEs = null;
let appState = { authed: false };

async function bootstrap() {
  let setup;
  try {
    setup = await api('GET', '/api/setup/status');
  } catch (e) {
    showFatal('与服务通信失败: ' + e.message);
    return;
  }
  if (!setup.initialized) {
    openModal('setup');
    return;
  }

  try {
    appState.authed = true;
    await refreshOverview();
    await refreshMappings();
    await refreshSocks5();
    await refreshDDNS();
    await refreshNotify();
    await refreshBanlist();
    await refreshTasks();
    startLogStream();
    closeModals();
  } catch (e) {
    if (e.status === 401) {
      openModal('login');
      return;
    }
    showFatal(e.message);
  }
}

async function refreshOverview() {
  // brief blue pulse on the auto-refresh dot during the in-flight request
  const ar = $('#auto-refresh');
  if (ar) ar.classList.add('refreshing');
  let ov;
  try { ov = await api('GET', '/api/overview'); } catch { if (ar) ar.classList.remove('refreshing'); return; }
  if (ar) setTimeout(() => ar.classList.remove('refreshing'), 250);
  const d = ov.daemon || {};
  const r = ov.router || {};
  const n = ov.network || {};
  const s = ov.services || {};
  const t = ov.traffic || {};
  const dd = ov.ddns || {};
  const nt = ov.notify || {};

  $('#username').textContent = d.username || '—';
  $('#version').textContent = d.number ? 'v' + d.number : '';

  // Network group
  $('#ov-wan-ipv6').textContent = n.wan_ipv6 || '(未取到)';
  $('#ov-ui-bind').textContent = n.ui_bind_addr || '—';
  $('#ov-ddns-domain').textContent = dd.domain || '—';
  setDDNSStatus(dd);

  // Daemon group — primary vs dim tiers
  $('#ov-daemon-version').textContent = 'v' + (d.number || '0.0.0');
  $('#ov-daemon-uptime').textContent = fmtDuration(d.uptime_seconds || 0);
  if (d.started_at) {
    const startedEl = $('#ov-daemon-started');
    startedEl.textContent = fmtRelTime(d.started_at);
    startedEl.title = new Date(d.started_at).toLocaleString();
  } else {
    $('#ov-daemon-started').textContent = '—';
  }

  const buildEl = $('#ov-daemon-build');
  if (d.commit && d.commit !== 'dev') {
    buildEl.textContent = d.commit;
    buildEl.title = d.build_time && d.build_time !== 'unknown' ? '构建于 ' + d.build_time : '';
  } else {
    buildEl.textContent = '本地构建';
    buildEl.title = '未通过 scripts/build.sh 注入构建元数据';
  }
  $('#ov-daemon-pid').textContent = d.pid || '—';
  $('#ov-config-version').textContent = d.config_version != null ? 'v' + d.config_version : '—';

  // Router group
  $('#ov-router-hostname').textContent = r.hostname || '—';
  $('#ov-router-os').textContent = r.os_description || (r.go_os === 'linux' ? '(未识别 OpenWrt)' : `${r.go_os} (开发主机)`);
  $('#ov-router-kernel-arch').textContent = [r.kernel_release, r.go_arch].filter(Boolean).join(' · ') || '—';
  $('#ov-router-uptime').textContent = r.uptime_seconds > 0 ? fmtDuration(r.uptime_seconds) : '—';
  $('#ov-router-load').textContent = (r.load_1 || r.load_5 || r.load_15)
    ? `${r.load_1.toFixed(2)} / ${r.load_5.toFixed(2)} / ${r.load_15.toFixed(2)}`
    : '—';
  if (r.mem_total_kb > 0) {
    const used = r.mem_total_kb - r.mem_available_kb;
    const pct = Math.round(used * 100 / r.mem_total_kb);
    $('#ov-router-mem').textContent = `${fmtKB(used)} / ${fmtKB(r.mem_total_kb)} (${pct}%)`;
  } else {
    $('#ov-router-mem').textContent = '—';
  }

  // Services / traffic group
  $('#ov-mappings').textContent = `${s.mappings_active} / ${s.mappings_total} active`;
  $('#ov-socks5').textContent = `${s.socks5_active} / ${s.socks5_total} active`;
  $('#ov-active').textContent = t.active_connections || 0;
  $('#ov-accepted').textContent = `${t.total_accepted || 0}${t.total_errors ? ` · 失败 ${t.total_errors}` : ''}`;
  $('#ov-bytes').innerHTML =
    `<span class="arrow-down" title="入站 (client → server)">↓ ${escape(fmtBytes(t.bytes_in || 0))}</span>` +
    ` <span class="sub">·</span> ` +
    `<span class="arrow-up" title="出站 (server → client)">↑ ${escape(fmtBytes(t.bytes_out || 0))}</span>`;
  $('#ov-notify').innerHTML = nt.enabled
    ? '<span class="ov-status-ok">✓ 已启用</span>'
    : '<span class="ov-status-off">✗ 未启用</span>';

  setOverviewTotals({ router: r, daemon: d, traffic: t });
  const ddnsHealth = computeDDNSHealth(dd);
  setHeaderStatusPill({ daemon: d, network: n, ddns: dd, ddnsHealth });
  setAlertBar({ ddns: dd });
}

// computeDDNSHealth returns one of:
//   'off'      — DDNS disabled
//   'pending'  — enabled but never synced yet
//   'ok'       — synced within 2× interval
//   'lag-warn' — synced 2×–4× interval ago, no explicit error
//   'lag-err'  — synced > 4× interval ago, no explicit error (severely stale)
//   'err'      — explicit last_error set (real failure from the sync goroutine)
//
// 'err' and 'lag-err' look similar (both red) but carry different labels because
// they represent different operator actions: 'err' → check credentials/network,
// 'lag-err' → check if the daemon's DDNS goroutine is alive.
function computeDDNSHealth(dd) {
  if (!dd || !dd.enabled) return 'off';
  if (dd.last_error) return 'err';
  if (!dd.last_synced_at) return 'pending';
  const lagSec = Math.max(0, (Date.now() - new Date(dd.last_synced_at).getTime()) / 1000);
  const interval = dd.interval_seconds && dd.interval_seconds > 0 ? dd.interval_seconds : 300;
  if (lagSec > interval * 4) return 'lag-err';
  if (lagSec > interval * 2) return 'lag-warn';
  return 'ok';
}

function setOverviewTotals({ router, daemon, traffic }) {
  const parts = [];
  if (router && router.uptime_seconds > 0) parts.push(`路由器 ${fmtDuration(router.uptime_seconds)}`);
  if (daemon && daemon.uptime_seconds != null) parts.push(`守护进程 ${fmtDuration(daemon.uptime_seconds)}`);
  const dn = traffic && traffic.bytes_in ? fmtBytes(traffic.bytes_in) : null;
  const up = traffic && traffic.bytes_out ? fmtBytes(traffic.bytes_out) : null;
  if (dn || up) parts.push(`↓ ${dn || '0 B'} · ↑ ${up || '0 B'}`);
  $('#overview-totals').innerHTML = parts.length
    ? parts.map(escape).join('<span class="sep">·</span>')
    : '—';
}

// alert bar: show banner when there's a serious issue user must see.
// Currently triggered only on DDNS error. dismissed-error key persists in
// sessionStorage so the same error doesn't keep popping after the user closes it.
function setAlertBar({ ddns }) {
  const bar = $('#alert-bar');
  const txt = $('#alert-text');
  if (ddns && ddns.enabled && ddns.last_error) {
    const dismissedKey = sessionStorage.getItem('alert-dismissed') || '';
    const sig = 'ddns:' + ddns.last_error;
    if (dismissedKey === sig) {
      bar.classList.add('hidden');
      return;
    }
    txt.textContent = 'DDNS 同步失败:' + ddns.last_error;
    bar.dataset.sig = sig;
    bar.classList.remove('hidden');
    return;
  }
  // no active issue → clear any dismissal record + hide
  sessionStorage.removeItem('alert-dismissed');
  bar.classList.add('hidden');
}

function setDDNSStatus(dd) {
  const el = $('#ov-ddns-status');
  const health = computeDDNSHealth(dd);
  if (health === 'off') {
    el.innerHTML = '<span class="ov-status-off">未启用</span>';
    return;
  }
  if (health === 'err' && dd.last_error) {
    el.innerHTML = `<span class="ov-status-err" title="${escape(dd.last_error)}">⚠ ${escape(truncate(dd.last_error, 60))}</span>`;
    return;
  }
  if (health === 'pending') {
    el.innerHTML = '<span class="ov-status-warn">等待首次同步</span>';
    return;
  }
  if (dd.last_synced_ip && dd.last_synced_at) {
    const abs = new Date(dd.last_synced_at).toLocaleString();
    const interval = dd.interval_seconds || 300;
    const cls = health === 'ok' ? 'ov-status-ok'
              : health === 'lag-warn' ? 'ov-status-warn'
              : 'ov-status-err';
    const icon = health === 'ok' ? '✓' : health === 'lag-warn' ? '◔' : '◴';
    const lagSec = Math.max(0, (Date.now() - new Date(dd.last_synced_at).getTime()) / 1000);
    const tipParts = [`同步于 ${abs}`, `间隔 ${interval}s`, `健康阈值 ×2/×4`];
    if (health !== 'ok') tipParts.push(`滞后 ${fmtDuration(Math.floor(lagSec))}（守护进程 DDNS 循环可能未运行）`);
    const tip = tipParts.join(' · ');
    el.innerHTML =
      `<span class="ov-line">` +
        `<span class="${cls}" title="${escape(tip)}">${icon} ${escape(dd.last_synced_ip)}</span>` +
        `<span class="sub" title="${escape(tip)}">${escape(fmtRelTime(dd.last_synced_at))}</span>` +
      `</span>`;
    return;
  }
  el.innerHTML = '<span class="ov-status-warn">等待首次同步</span>';
}

function truncate(s, n) {
  s = String(s || '');
  return s.length <= n ? s : s.slice(0, n - 1) + '…';
}

function setHeaderStatusPill({ daemon, network, ddns, ddnsHealth }) {
  const pill = $('#status-pill');
  const text = $('#status-text');
  let cls = 'ok';
  let label;
  const title = [];

  if (!network || !network.wan_ipv6) {
    cls = 'warn';
    label = 'WAN IPv6 未取到';
  } else if (ddnsHealth === 'err') {
    cls = 'err';
    label = 'DDNS 错误';
    if (ddns && ddns.last_error) title.push('DDNS: ' + ddns.last_error);
  } else if (ddnsHealth === 'lag-err') {
    cls = 'err';
    label = 'DDNS 滞后';
    title.push('WAN IPv6: ' + network.wan_ipv6);
    title.push('DDNS 同步严重滞后(> 4× 间隔),守护进程 DDNS 循环可能未运行');
  } else if (ddnsHealth === 'lag-warn') {
    cls = 'warn';
    label = network.wan_ipv6;
    title.push('WAN IPv6: ' + network.wan_ipv6);
    title.push('DDNS 同步滞后(> 2× 间隔)');
  } else if (ddnsHealth === 'pending') {
    cls = 'warn';
    label = network.wan_ipv6;
    title.push('WAN IPv6: ' + network.wan_ipv6);
    title.push('DDNS 等待首次同步');
  } else {
    cls = 'ok';
    label = network.wan_ipv6;
    title.push('WAN IPv6: ' + network.wan_ipv6);
  }
  if (daemon && daemon.uptime_seconds != null) {
    title.unshift('运行 ' + fmtDuration(daemon.uptime_seconds));
  }
  pill.classList.remove('ok', 'warn', 'err');
  pill.classList.add(cls);
  text.textContent = label;
  pill.title = title.join(' · ');
}

// --- Mappings (forward) ---

async function refreshMappings() {
  const list = await api('GET', '/api/mappings') || [];
  const tbody = $('#mappings-tbody');
  tbody.innerHTML = '';
  if (list.length === 0) {
    $('#mappings-empty').classList.remove('hidden');
    $('#mappings-table').classList.add('hidden');
    return;
  }
  $('#mappings-empty').classList.add('hidden');
  $('#mappings-table').classList.remove('hidden');

  for (const m of list) {
    const stats = m.stats || {};
    const tr = document.createElement('tr');
    tr.innerHTML = `
      <td><label class="switch"><input type="checkbox" ${m.enabled ? 'checked' : ''} data-id="${escape(m.id)}" class="m-toggle"><span class="slider"></span></label></td>
      <td>${escape(m.label || m.id)}</td>
      <td><code>${escape(m.protocol)}</code></td>
      <td><code>${m.listen_port}</code></td>
      <td><code>${escape(m.target_ip)}:${m.target_port}</code></td>
      <td class="num">${stats.active || 0} / ${stats.accepted || 0}</td>
      <td class="num">${fmtBytes(stats.bytes_in)} / ${fmtBytes(stats.bytes_out)}</td>
      <td class="ts">${new Date(m.updated_at).toLocaleString()}</td>
      <td class="ops">
        <button data-id="${escape(m.id)}" data-label="${escape(m.label || m.id)}" class="m-history btn-edit">📈</button>
        <button data-id="${escape(m.id)}" class="m-edit btn-edit">编辑</button>
        <button data-id="${escape(m.id)}" class="m-del danger">删除</button>
      </td>
    `;
    tbody.appendChild(tr);
  }
  for (const b of $$('.m-history')) {
    b.onclick = () => openHistory(b.dataset.id, b.dataset.label);
  }
  for (const cb of $$('.m-toggle')) {
    cb.onchange = async (e) => {
      const id = e.target.dataset.id;
      const orig = !e.target.checked;
      try {
        await api('PATCH', `/api/mappings/${encodeURIComponent(id)}`, { enabled: e.target.checked });
        await refreshOverview();
      } catch (err) {
        e.target.checked = orig;
        alert('更新失败: ' + err.message);
      }
    };
  }
  for (const b of $$('.m-edit')) {
    b.onclick = () => editMapping(list.find(x => x.id === b.dataset.id));
  }
  for (const b of $$('.m-del')) {
    b.onclick = () => deleteMapping(b.dataset.id);
  }
}

function editMapping(m) {
  const form = $('#form-mapping');
  if (m) {
    $('#modal-mapping-title').textContent = '编辑端口映射';
    form.id.value = m.id;
    form.label.value = m.label || '';
    form.protocol.value = m.protocol || 'tcp';
    form.listen_port.value = m.listen_port;
    form.target_ip.value = m.target_ip || '';
    form.target_port.value = m.target_port || '';
    form.enabled.checked = m.enabled;
  } else {
    $('#modal-mapping-title').textContent = '新增端口映射';
    form.reset();
    form.id.value = '';
    form.enabled.checked = true;
    form.protocol.value = 'tcp';
  }
  $('#mapping-err').textContent = '';
  openModal('mapping');
}

async function deleteMapping(id) {
  if (!confirm(`确认删除映射 "${id}"？外部端口将立即关闭。`)) return;
  try {
    await api('DELETE', `/api/mappings/${encodeURIComponent(id)}`);
    await refreshMappings();
    await refreshOverview();
  } catch (e) {
    alert('删除失败: ' + e.message);
  }
}

$('#btn-add-mapping').onclick = () => editMapping(null);
$('#btn-mapping-cancel').onclick = closeModals;

$('#form-mapping').onsubmit = async (e) => {
  e.preventDefault();
  const form = e.target;
  const body = {
    label: form.label.value.trim(),
    protocol: form.protocol.value,
    listen_port: parseInt(form.listen_port.value, 10),
    target_ip: form.target_ip.value.trim(),
    target_port: parseInt(form.target_port.value, 10),
    enabled: form.enabled.checked,
  };
  const id = form.id.value;
  try {
    if (id) {
      await api('PUT', `/api/mappings/${encodeURIComponent(id)}`, body);
    } else {
      await api('POST', '/api/mappings', body);
    }
    closeModals();
    await refreshMappings();
    await refreshOverview();
  } catch (err) {
    $('#mapping-err').textContent = err.message;
  }
};

// --- SOCKS5 ---

async function refreshSocks5() {
  const list = await api('GET', '/api/socks5') || [];
  const tbody = $('#socks5-tbody');
  tbody.innerHTML = '';
  if (list.length === 0) {
    $('#socks5-empty').classList.remove('hidden');
    $('#socks5-table').classList.add('hidden');
    return;
  }
  $('#socks5-empty').classList.add('hidden');
  $('#socks5-table').classList.remove('hidden');

  for (const s of list) {
    const stats = s.stats || {};
    const tr = document.createElement('tr');
    tr.innerHTML = `
      <td><label class="switch"><input type="checkbox" ${s.enabled ? 'checked' : ''} data-id="${escape(s.id)}" class="s-toggle"><span class="slider"></span></label></td>
      <td>${escape(s.label || s.id)}</td>
      <td><code>${s.listen_port}</code></td>
      <td><code>${escape(s.username)}</code></td>
      <td class="num">${stats.active || 0} / ${stats.accepted || 0}</td>
      <td class="num">${fmtBytes(stats.bytes_in)} / ${fmtBytes(stats.bytes_out)}</td>
      <td class="ts">${new Date(s.updated_at).toLocaleString()}</td>
      <td class="ops">
        <button data-id="${escape(s.id)}" data-label="${escape(s.label || s.id)}" class="s-export btn-edit">📥</button>
        <button data-id="${escape(s.id)}" data-label="${escape(s.label || s.id)}" class="s-history btn-edit">📈</button>
        <button data-id="${escape(s.id)}" class="s-edit btn-edit">编辑</button>
        <button data-id="${escape(s.id)}" class="s-del danger">删除</button>
      </td>
    `;
    tbody.appendChild(tr);
  }
  for (const b of $$('.s-export')) {
    b.onclick = () => openExport(b.dataset.id, b.dataset.label);
  }
  for (const b of $$('.s-history')) {
    b.onclick = () => openHistory(b.dataset.id, b.dataset.label);
  }
  for (const cb of $$('.s-toggle')) {
    cb.onchange = async (e) => {
      const id = e.target.dataset.id;
      const orig = !e.target.checked;
      try {
        await api('PATCH', `/api/socks5/${encodeURIComponent(id)}`, { enabled: e.target.checked });
        await refreshSocks5();
      } catch (err) {
        e.target.checked = orig;
        alert('更新失败: ' + err.message);
      }
    };
  }
  for (const b of $$('.s-edit')) {
    b.onclick = () => editSocks5(list.find(x => x.id === b.dataset.id));
  }
  for (const b of $$('.s-del')) {
    b.onclick = () => deleteSocks5(b.dataset.id);
  }
}

function editSocks5(s) {
  const form = $('#form-socks5');
  if (s) {
    $('#modal-socks5-title').textContent = '编辑 SOCKS5 代理';
    form.id.value = s.id;
    form.label.value = s.label || '';
    form.listen_port.value = s.listen_port;
    form.username.value = s.username || '';
    form.password.value = s.password || '';
    form.password.placeholder = '留空保留旧值';
    form.password.required = false;
    form.enabled.checked = s.enabled;
  } else {
    $('#modal-socks5-title').textContent = '新增 SOCKS5 代理';
    form.reset();
    form.id.value = '';
    form.password.required = true;
    form.password.placeholder = '建议 ≥ 12 位强密码';
    form.enabled.checked = true;
  }
  $('#socks5-err').textContent = '';
  openModal('socks5');
}

async function deleteSocks5(id) {
  if (!confirm(`确认删除 SOCKS5 "${id}"？外部端口将立即关闭。`)) return;
  try {
    await api('DELETE', `/api/socks5/${encodeURIComponent(id)}`);
    await refreshSocks5();
    await refreshOverview();
  } catch (e) {
    alert('删除失败: ' + e.message);
  }
}

$('#btn-add-socks5').onclick = () => editSocks5(null);
$('#btn-socks5-cancel').onclick = closeModals;

$('#form-socks5').onsubmit = async (e) => {
  e.preventDefault();
  const form = e.target;
  const id = form.id.value;
  const body = {
    label: form.label.value.trim(),
    listen_port: parseInt(form.listen_port.value, 10),
    username: form.username.value.trim(),
    enabled: form.enabled.checked,
  };
  if (form.password.value) {
    body.password = form.password.value;
  } else if (!id) {
    $('#socks5-err').textContent = '新建必须填密码';
    return;
  }
  try {
    if (id) {
      await api('PATCH', `/api/socks5/${encodeURIComponent(id)}`, body);
    } else {
      await api('POST', '/api/socks5', body);
    }
    closeModals();
    await refreshSocks5();
    await refreshOverview();
  } catch (err) {
    $('#socks5-err').textContent = err.message;
  }
};

// --- Login / Setup / Password ---

$('#form-login').onsubmit = async (e) => {
  e.preventDefault();
  const form = e.target;
  try {
    await api('POST', '/api/auth/login', {
      username: form.username.value,
      password: form.password.value,
    });
    closeModals();
    await bootstrap();
  } catch (err) {
    $('#login-err').textContent = err.message;
  }
};

$('#form-setup').onsubmit = async (e) => {
  e.preventDefault();
  const form = e.target;
  try {
    await api('POST', '/api/setup/init', {
      username: form.username.value,
      password: form.password.value,
    });
    await api('POST', '/api/auth/login', {
      username: form.username.value,
      password: form.password.value,
    });
    closeModals();
    await bootstrap();
  } catch (err) {
    $('#setup-err').textContent = err.message;
  }
};

$('#btn-logout').onclick = async () => {
  try { await api('POST', '/api/auth/logout'); } catch {}
  if (logEs) { logEs.close(); logEs = null; }
  location.reload();
};

$('#btn-settings').onclick = () => {
  $('#password-err').textContent = '';
  $('#form-password').reset();
  openModal('settings');
};
$('#btn-settings-close').onclick = closeModals;

$('#form-password').onsubmit = async (e) => {
  e.preventDefault();
  const form = e.target;
  try {
    await api('POST', '/api/auth/change-password', {
      old_password: form.old_password.value,
      new_password: form.new_password.value,
    });
    alert('密码已修改，请重新登录');
    location.reload();
  } catch (err) {
    $('#password-err').textContent = err.message;
  }
};

$('#btn-clear-log').onclick = () => { $('#logs-pre').textContent = ''; };

// --- DDNS ---

async function refreshDDNS() {
  let d;
  try { d = await api('GET', '/api/ddns'); } catch (e) { return; }
  $('#ddns-status').textContent = d.enabled ? '✓ 已启用' : '✗ 未启用';
  $('#ddns-status').style.color = d.enabled ? '#27ae60' : '#888';
  $('#ddns-domain').textContent = d.domain || '—';
  $('#ddns-ip').textContent = d.last_synced_ip || '—';
  $('#ddns-last').textContent = d.last_synced_at ? new Date(d.last_synced_at).toLocaleString() : '—';
  if (d.last_error) {
    $('#ddns-err-row').classList.remove('hidden');
    $('#ddns-err').textContent = d.last_error;
  } else {
    $('#ddns-err-row').classList.add('hidden');
  }
}

$('#btn-ddns-sync').onclick = async () => {
  $('#btn-ddns-sync').disabled = true;
  try {
    await api('POST', '/api/ddns/sync');
    await refreshDDNS();
  } catch (e) {
    alert('同步失败: ' + e.message);
    await refreshDDNS();
  } finally {
    $('#btn-ddns-sync').disabled = false;
  }
};

$('#btn-ddns-edit').onclick = async () => {
  const d = await api('GET', '/api/ddns');
  const form = $('#form-ddns');
  form.enabled.checked = !!d.enabled;
  form.domain.value = d.domain || '';
  form.ttl.value = d.ttl || 600;
  form.interval_seconds.value = d.interval_seconds || 300;
  form.access_key_id.value = d.access_key_id || '';
  form.access_key_secret.value = d.access_key_secret || '';
  form.access_key_secret.placeholder = '请输入 Secret（留空保留旧值）';
  $('#ddns-form-msg').textContent = '';
  $('#ddns-form-err').textContent = '';
  openModal('ddns');
};

$('#btn-ddns-cancel').onclick = closeModals;

function ddnsBodyFromForm() {
  const f = $('#form-ddns');
  return {
    enabled: f.enabled.checked,
    domain: f.domain.value.trim(),
    ttl: parseInt(f.ttl.value, 10) || 600,
    interval_seconds: parseInt(f.interval_seconds.value, 10) || 300,
    access_key_id: f.access_key_id.value.trim(),
    access_key_secret: f.access_key_secret.value,
  };
}

$('#btn-ddns-test').onclick = async () => {
  const body = ddnsBodyFromForm();
  $('#ddns-form-err').textContent = '';
  $('#ddns-form-msg').textContent = '测试中…';
  try {
    const r = await api('POST', '/api/ddns/test', body);
    const n = (r.existing_records || []).length;
    $('#ddns-form-msg').textContent =
      `凭据 OK；zone=${r.zone}, rr=${r.rr}, 已存在 ${n} 条 AAAA 记录`;
  } catch (e) {
    $('#ddns-form-msg').textContent = '';
    $('#ddns-form-err').textContent = '测试失败: ' + e.message;
  }
};

$('#form-ddns').onsubmit = async (e) => {
  e.preventDefault();
  const body = ddnsBodyFromForm();
  try {
    await api('PUT', '/api/ddns', body);
    closeModals();
    await refreshDDNS();
  } catch (err) {
    $('#ddns-form-err').textContent = err.message;
  }
};

// --- Notify ---

async function refreshNotify() {
  let n;
  try { n = await api('GET', '/api/notify'); } catch (e) { return; }
  $('#notify-status').textContent = n.enabled ? '✓ 已启用' : '✗ 未启用';
  $('#notify-status').style.color = n.enabled ? '#27ae60' : '#888';
  $('#notify-url').textContent = n.webhook_url || '—';
  const evs = [];
  if (n.notify_on_daemon_start) evs.push('启动');
  if (n.notify_on_ddns_change) evs.push('DDNS 变化');
  if (n.notify_on_ddns_error) evs.push('DDNS 错误');
  if (n.notify_on_socks5_ban) evs.push('SOCKS5 封禁');
  $('#notify-events').textContent = evs.length ? evs.join(' / ') : '（全部关闭）';
}

$('#btn-notify-edit').onclick = async () => {
  const n = await api('GET', '/api/notify');
  const form = $('#form-notify');
  form.enabled.checked = !!n.enabled;
  form.webhook_url.value = n.webhook_url || '';
  form.webhook_url.placeholder = '请输入 Webhook URL（留空保留旧值）';
  form.notify_on_daemon_start.checked = !!n.notify_on_daemon_start;
  form.notify_on_ddns_change.checked = !!n.notify_on_ddns_change;
  form.notify_on_ddns_error.checked = !!n.notify_on_ddns_error;
  form.notify_on_socks5_ban.checked = !!n.notify_on_socks5_ban;
  $('#notify-form-msg').textContent = '';
  $('#notify-form-err').textContent = '';
  openModal('notify');
};

$('#btn-notify-cancel').onclick = closeModals;

function notifyBodyFromForm() {
  const f = $('#form-notify');
  return {
    enabled: f.enabled.checked,
    provider: 'feishu',
    webhook_url: f.webhook_url.value.trim(),
    notify_on_daemon_start: f.notify_on_daemon_start.checked,
    notify_on_ddns_change: f.notify_on_ddns_change.checked,
    notify_on_ddns_error: f.notify_on_ddns_error.checked,
    notify_on_socks5_ban: f.notify_on_socks5_ban.checked,
  };
}

$('#btn-notify-send-test').onclick = async () => {
  const body = notifyBodyFromForm();
  $('#notify-form-err').textContent = '';
  $('#notify-form-msg').textContent = '发送中…';
  try {
    await api('POST', '/api/notify/test', { webhook_url: body.webhook_url });
    $('#notify-form-msg').textContent = '✓ 测试消息发送成功';
  } catch (e) {
    $('#notify-form-msg').textContent = '';
    $('#notify-form-err').textContent = '失败: ' + e.message;
  }
};

$('#btn-notify-test').onclick = async () => {
  $('#btn-notify-test').disabled = true;
  try {
    await api('POST', '/api/notify/test');
    alert('测试通知已发送');
  } catch (e) {
    alert('发送失败: ' + e.message);
  } finally {
    $('#btn-notify-test').disabled = false;
  }
};

$('#form-notify').onsubmit = async (e) => {
  e.preventDefault();
  const body = notifyBodyFromForm();
  try {
    await api('PUT', '/api/notify', body);
    closeModals();
    await refreshNotify();
  } catch (err) {
    $('#notify-form-err').textContent = err.message;
  }
};

function startLogStream() {
  if (logEs) logEs.close();
  logEs = new EventSource('/api/logs');
  const pre = $('#logs-pre');
  logEs.onmessage = (e) => {
    pre.textContent += e.data + '\n';
    const lines = pre.textContent.split('\n');
    if (lines.length > 200) {
      pre.textContent = lines.slice(-200).join('\n');
    }
    pre.scrollTop = pre.scrollHeight;
  };
  logEs.onerror = () => {};
}

// --- P7: Export client config ---

let exportCtx = { id: '', label: '', fmt: 'uri', text: '' };

function openExport(id, label) {
  exportCtx = { id, label, fmt: 'uri', text: '' };
  $('#modal-export-title').textContent = '导出 — ' + label;
  $$('#modal-export .tab').forEach(t => t.classList.toggle('active', t.dataset.fmt === 'uri'));
  loadExport('uri');
  openModal('export');
}
async function loadExport(fmt) {
  exportCtx.fmt = fmt;
  $('#export-pre').textContent = '加载中…';
  const r = await fetch(`/api/socks5-clientconfig/${encodeURIComponent(exportCtx.id)}?format=${fmt}`);
  if (!r.ok) {
    $('#export-pre').textContent = '加载失败: HTTP ' + r.status;
    return;
  }
  exportCtx.text = await r.text();
  $('#export-pre').textContent = exportCtx.text;
}
$$('#modal-export .tab').forEach(t => {
  t.onclick = () => {
    $$('#modal-export .tab').forEach(x => x.classList.remove('active'));
    t.classList.add('active');
    loadExport(t.dataset.fmt);
  };
});
$('#btn-export-copy').onclick = async () => {
  try {
    await navigator.clipboard.writeText(exportCtx.text);
    alert('已复制');
  } catch {
    // fallback: select text
    const r = document.createRange();
    r.selectNode($('#export-pre'));
    window.getSelection().removeAllRanges();
    window.getSelection().addRange(r);
    alert('已选中（手动 Cmd/Ctrl+C 复制）');
  }
};
$('#btn-export-download').onclick = () => {
  window.open(`/api/socks5-clientconfig/${encodeURIComponent(exportCtx.id)}?format=${exportCtx.fmt}&download=1`);
};
$('#btn-export-close').onclick = closeModals;

// --- P8: History chart ---

let historyCtx = { id: '', label: '', hours: 24 };

function openHistory(id, label) {
  historyCtx = { id, label, hours: 24 };
  $('#modal-history-title').textContent = '历史流量 — ' + label;
  $$('#history-range-tabs .tab').forEach(t => t.classList.toggle('active', t.dataset.hours === '24'));
  drawHistory();
  openModal('history');
}
$$('#history-range-tabs .tab').forEach(t => {
  t.onclick = () => {
    $$('#history-range-tabs .tab').forEach(x => x.classList.remove('active'));
    t.classList.add('active');
    historyCtx.hours = parseInt(t.dataset.hours, 10);
    drawHistory();
  };
});
$('#btn-history-close').onclick = closeModals;

async function drawHistory() {
  const svg = $('#history-chart');
  svg.innerHTML = '<text x="360" y="120" text-anchor="middle" class="legend">加载中…</text>';
  $('#history-empty').classList.add('hidden');
  let d;
  try {
    d = await api('GET', `/api/history?id=${encodeURIComponent(historyCtx.id)}&hours=${historyCtx.hours}`);
  } catch (e) {
    svg.innerHTML = `<text x="360" y="120" text-anchor="middle" class="legend">加载失败: ${escape(e.message)}</text>`;
    return;
  }
  const labels = d.labels || [];
  const inn = d.bytes_in_delta || [];
  const out = d.bytes_out_delta || [];
  if (labels.length < 2) {
    svg.innerHTML = '';
    $('#history-empty').classList.remove('hidden');
    return;
  }

  // Compute layout
  const W = 720, H = 240;
  const padL = 60, padR = 16, padT = 18, padB = 38;
  const maxV = Math.max(1, ...inn, ...out);
  const xStep = (W - padL - padR) / Math.max(1, labels.length - 1);
  const yScale = (H - padT - padB) / maxV;

  // Build path strings
  const pathFrom = (arr) => {
    return arr.map((v, i) => {
      const x = padL + i * xStep;
      const y = H - padB - v * yScale;
      return (i === 0 ? 'M' : 'L') + x.toFixed(1) + ',' + y.toFixed(1);
    }).join(' ');
  };

  // y-axis ticks (5)
  const ticks = [];
  for (let i = 0; i <= 4; i++) {
    const v = maxV * i / 4;
    const y = H - padB - v * yScale;
    ticks.push(`<line class="grid" x1="${padL}" y1="${y}" x2="${W - padR}" y2="${y}"></line>` +
      `<text class="axis-text" x="${padL - 6}" y="${y + 3}" text-anchor="end">${fmtBytes(v)}</text>`);
  }
  // x-axis labels (max ~6)
  const stride = Math.max(1, Math.floor(labels.length / 6));
  const xLabels = [];
  for (let i = 0; i < labels.length; i += stride) {
    const x = padL + i * xStep;
    xLabels.push(`<text class="axis-text" x="${x}" y="${H - padB + 14}" text-anchor="middle">${escape(labels[i])}</text>`);
  }

  svg.innerHTML = `
    ${ticks.join('')}
    <line class="axis" x1="${padL}" y1="${padT}" x2="${padL}" y2="${H - padB}"></line>
    <line class="axis" x1="${padL}" y1="${H - padB}" x2="${W - padR}" y2="${H - padB}"></line>
    <path class="line-in" d="${pathFrom(inn)}"></path>
    <path class="line-out" d="${pathFrom(out)}"></path>
    ${xLabels.join('')}
    <text class="legend" x="${W - padR}" y="${padT + 4}" text-anchor="end">
      <tspan fill="#4a6fa5">━</tspan> 入站
      <tspan dx="10" fill="#27ae60">━</tspan> 出站
    </text>
  `;
}

// --- P9: Ban list ---

async function refreshBanlist() {
  let list;
  try { list = await api('GET', '/api/banlist'); } catch { return; }
  const tbody = $('#banlist-tbody');
  if (!list || list.length === 0) {
    $('#banlist-section').classList.add('hidden');
    return;
  }
  $('#banlist-section').classList.remove('hidden');
  tbody.innerHTML = '';
  for (const e of list) {
    const tr = document.createElement('tr');
    const status = e.is_banned ? '🚫 已封禁' : '⚠️ 仅记录失败';
    const unbansAt = e.unbans_at ? new Date(e.unbans_at).toLocaleString() : '—';
    const lastFail = e.last_fail_at ? new Date(e.last_fail_at).toLocaleString() : '—';
    tr.innerHTML = `
      <td><code>${escape(e.ip)}</code></td>
      <td class="num">${e.fails}</td>
      <td>${status}</td>
      <td class="ts">${unbansAt}</td>
      <td class="ts">${lastFail}</td>
      <td><button data-ip="${escape(e.ip)}" class="b-unban btn-edit">解除</button></td>
    `;
    tbody.appendChild(tr);
  }
  for (const b of $$('.b-unban')) {
    b.onclick = async () => {
      if (!confirm('确认解除封禁 ' + b.dataset.ip + '?')) return;
      try {
        await api('DELETE', '/api/banlist/' + encodeURIComponent(b.dataset.ip));
        await refreshBanlist();
      } catch (e) { alert('失败: ' + e.message); }
    };
  }
}
$('#btn-banlist-refresh').onclick = () => refreshBanlist();
$('#btn-overview-refresh').onclick = () => refreshOverview();
$('#alert-close').onclick = () => {
  const bar = $('#alert-bar');
  if (bar.dataset.sig) sessionStorage.setItem('alert-dismissed', bar.dataset.sig);
  bar.classList.add('hidden');
};

// --- Tasks ---

async function refreshTasks() {
  let list;
  try {
    list = await api('GET', '/api/tasks') || [];
  } catch (e) {
    return;
  }
  const tbody = $('#tasks-tbody');
  if (!tbody) return;
  tbody.innerHTML = '';
  if (list.length === 0) {
    $('#tasks-empty').classList.remove('hidden');
    $('#tasks-table').classList.add('hidden');
    return;
  }
  $('#tasks-empty').classList.add('hidden');
  $('#tasks-table').classList.remove('hidden');

  for (const t of list) {
    const tr = document.createElement('tr');
    const typeBadge = t.type === 'http' 
      ? '<span class="badge badge-http">HTTP</span>' 
      : '<span class="badge badge-shell">SHELL</span>';

    let statusBadge = '<span class="status-badge never">从未执行</span>';
    if (t.last_run_status === 'success') {
      statusBadge = `<span class="status-badge success">成功 (${t.last_run_duration_ms}ms)</span>`;
    } else if (t.last_run_status === 'failure') {
      statusBadge = `<span class="status-badge failure" title="${escape(t.last_run_error || '')}">失败</span>`;
    } else if (t.last_run_status === 'running') {
      statusBadge = `<span class="status-badge running">运行中…</span>`;
    }

    const lastRunTime = t.last_run_at ? fmtRelTime(t.last_run_at) : '—';

    tr.innerHTML = `
      <td><label class="switch"><input type="checkbox" ${t.enabled ? 'checked' : ''} data-id="${escape(t.id)}" class="t-toggle"><span class="slider"></span></label></td>
      <td><strong>${escape(t.name)}</strong></td>
      <td>${typeBadge}</td>
      <td><code>${escape(t.cron)}</code></td>
      <td>${statusBadge} <span class="ts">(${lastRunTime})</span></td>
      <td class="ops">
        <button data-id="${escape(t.id)}" class="t-run btn-edit" title="立即测试执行">▶ 执行</button>
        <button data-id="${escape(t.id)}" data-name="${escape(t.name)}" class="t-history btn-edit" title="查看执行历史">📄 日志</button>
        <button data-id="${escape(t.id)}" class="t-edit btn-edit">编辑</button>
        <button data-id="${escape(t.id)}" class="t-del danger">删除</button>
      </td>
    `;
    tbody.appendChild(tr);
  }

  for (const b of $$('.t-run')) {
    b.onclick = () => runTaskNow(b.dataset.id, b);
  }
  for (const b of $$('.t-history')) {
    b.onclick = () => openTaskHistory(b.dataset.id, b.dataset.name);
  }
  for (const b of $$('.t-edit')) {
    b.onclick = () => {
      const task = list.find(x => x.id === b.dataset.id);
      if (task) openTaskModal(task);
    };
  }
  for (const b of $$('.t-del')) {
    b.onclick = () => deleteTask(b.dataset.id);
  }
  for (const cb of $$('.t-toggle')) {
    cb.onchange = async (e) => {
      const id = e.target.dataset.id;
      const orig = !e.target.checked;
      try {
        await api('PUT', `/api/tasks/${encodeURIComponent(id)}`, {
          ...list.find(x => x.id === id),
          enabled: e.target.checked
        });
        await refreshTasks();
      } catch (err) {
        e.target.checked = orig;
        alert('切换状态失败: ' + err.message);
      }
    };
  }
}

function openTaskModal(task) {
  const form = $('#form-task');
  $('#task-err').textContent = '';
  if (task) {
    $('#modal-task-title').textContent = '编辑计划任务';
    form.id.value = task.id;
    form.name.value = task.name || '';
    form.type.value = task.type || 'shell';
    form.timeout_seconds.value = task.timeout_seconds || 60;
    form.shell_command.value = task.shell_command || '';
    form.http_method.value = task.http_method || 'GET';
    form.http_url.value = task.http_url || '';
    form.http_headers.value = task.http_headers || '';
    form.http_body.value = task.http_body || '';
    form.cron.value = task.cron || '';
    form.notify_policy.value = task.notify_policy || 'on_failure';
    form.enabled.checked = task.enabled !== false;
  } else {
    $('#modal-task-title').textContent = '新增计划任务';
    form.reset();
    form.id.value = '';
    form.type.value = 'shell';
    form.timeout_seconds.value = 60;
    form.cron.value = '0 4 * * 1';
    form.notify_policy.value = 'on_failure';
    form.enabled.checked = true;
  }
  updateTaskTypeVisibility();
  openModal('task');
}

function updateTaskTypeVisibility() {
  const type = $('#task-type-select').value;
  if (type === 'http') {
    $('#task-shell-fields').classList.add('hidden');
    $('#task-http-fields').classList.remove('hidden');
  } else {
    $('#task-shell-fields').classList.remove('hidden');
    $('#task-http-fields').classList.add('hidden');
  }
}

$('#task-type-select').onchange = updateTaskTypeVisibility;

for (const btn of $$('.btn-cron-preset')) {
  btn.onclick = () => {
    $('#task-cron-input').value = btn.dataset.cron;
  };
}

$('#btn-task-add').onclick = () => openTaskModal(null);
$('#btn-task-refresh').onclick = () => refreshTasks();
$('#btn-task-cancel').onclick = () => closeModals();

$('#form-task').onsubmit = async (e) => {
  e.preventDefault();
  const form = e.target;
  const errEl = $('#task-err');
  errEl.textContent = '';
  const payload = {
    name: form.name.value.trim(),
    type: form.type.value,
    timeout_seconds: parseInt(form.timeout_seconds.value, 10) || 60,
    shell_command: form.shell_command.value.trim(),
    http_method: form.http_method.value,
    http_url: form.http_url.value.trim(),
    http_headers: form.http_headers.value.trim(),
    http_body: form.http_body.value.trim(),
    cron: form.cron.value.trim(),
    notify_policy: form.notify_policy.value,
    enabled: form.enabled.checked,
  };
  const id = form.id.value;
  try {
    if (id) {
      await api('PUT', `/api/tasks/${encodeURIComponent(id)}`, payload);
    } else {
      await api('POST', '/api/tasks', payload);
    }
    closeModals();
    await refreshTasks();
  } catch (err) {
    errEl.textContent = err.message;
  }
};

async function deleteTask(id) {
  if (!confirm('确认删除该任务？')) return;
  try {
    await api('DELETE', `/api/tasks/${encodeURIComponent(id)}`);
    await refreshTasks();
  } catch (err) {
    alert('删除失败: ' + err.message);
  }
}

async function runTaskNow(id, btn) {
  const origText = btn.textContent;
  btn.disabled = true;
  btn.textContent = '⏳ 执行中';
  try {
    const rec = await api('POST', `/api/tasks/${encodeURIComponent(id)}/run`);
    const statusText = rec.status === 'success' ? '成功' : '失败';
    alert(`任务执行完毕！\n状态: ${statusText}\n耗时: ${rec.duration_ms}ms\n结果码: ${rec.exit_code}`);
    await refreshTasks();
  } catch (err) {
    alert('触发执行失败: ' + err.message);
  } finally {
    btn.disabled = false;
    btn.textContent = origText;
  }
}

async function openTaskHistory(id, name) {
  $('#modal-task-history-title').textContent = `任务历史: ${name}`;
  const tbody = $('#task-history-tbody');
  tbody.innerHTML = '<tr><td colspan="6">加载中…</td></tr>';
  $('#task-history-empty').classList.add('hidden');
  $('#task-history-log-wrap').classList.add('hidden');
  $('#btn-task-history-copy').classList.add('hidden');
  openModal('task-history');

  let list = [];
  try {
    list = await api('GET', `/api/tasks/${encodeURIComponent(id)}/history`) || [];
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="6" class="err">加载失败: ${escape(e.message)}</td></tr>`;
    return;
  }

  if (list.length === 0) {
    tbody.innerHTML = '';
    $('#task-history-empty').classList.remove('hidden');
    return;
  }

  tbody.innerHTML = '';
  for (const r of list) {
    const tr = document.createElement('tr');
    const isSuccess = r.status === 'success';
    const trig = r.trigger_type === 'cron' ? '定时调度' : '手动测试';
    const statusBadge = isSuccess
      ? '<span class="status-badge success">成功</span>'
      : '<span class="status-badge failure">失败</span>';
    tr.innerHTML = `
      <td class="ts">${new Date(r.started_at).toLocaleString()}</td>
      <td>${trig}</td>
      <td>${statusBadge}</td>
      <td>${r.duration_ms}ms</td>
      <td><code>${r.exit_code}</code></td>
      <td><button class="h-view-log btn-edit">查看输出</button></td>
    `;
    const btn = tr.querySelector('.h-view-log');
    btn.onclick = () => {
      $('#task-history-log-wrap').classList.remove('hidden');
      $('#task-history-log').textContent = r.output || (r.error ? '错误: ' + r.error : '（无输出）');
      const copyBtn = $('#btn-task-history-copy');
      copyBtn.classList.remove('hidden');
      copyBtn.onclick = () => {
        navigator.clipboard.writeText(r.output || r.error || '');
        copyBtn.textContent = '已复制!';
        setTimeout(() => { copyBtn.textContent = '复制日志'; }, 1500);
      };
    };
    tbody.appendChild(tr);
  }
}

$('#btn-task-history-close').onclick = () => closeModals();

setInterval(() => {
  if (appState.authed) {
    refreshOverview().catch(() => {});
    refreshMappings().catch(() => {});
    refreshSocks5().catch(() => {});
    refreshDDNS().catch(() => {});
    refreshNotify().catch(() => {});
    refreshBanlist().catch(() => {});
    refreshTasks().catch(() => {});
  }
}, 5000);

bootstrap();
