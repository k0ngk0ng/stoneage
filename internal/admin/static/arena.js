(() => {
  "use strict";
  const root = document.getElementById("arena-monitor");
  if (!root) return;
  const el = id => document.getElementById(`arena-${id}`);
  let offset = 0, busy = false;
  const duration = ms => `${Math.floor(Math.max(0, ms) / 1000)} 秒`;
  const members = team => team.members.map(p => `${p.name || p.id}${p.online ? "" : "（离线）"}${p.strategy ? ` [${p.strategy}]` : ""}`).join("、");
  function rows(id, data, cells, empty) {
    const body = el(id); body.replaceChildren();
    if (!data.length) {
      const row = document.createElement("tr"), cell = document.createElement("td");
      cell.colSpan = id === "queues" ? 4 : 5; cell.textContent = empty;
      row.appendChild(cell); body.appendChild(row); return;
    }
    for (const value of data) {
      const row = document.createElement("tr");
      for (const text of cells(value)) { const cell = document.createElement("td"); cell.textContent = text; row.appendChild(cell); }
      body.appendChild(row);
    }
  }
  async function refresh() {
    if (busy || document.hidden) return;
    busy = true; el("prev").disabled = true; el("next").disabled = true;
    const controller = new AbortController(), timeout = setTimeout(() => controller.abort(), 5000);
    try {
      const response = await fetch(`/api/arena?offset=${offset}`, {cache: "no-store", signal: controller.signal});
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || "读取失败");
      if (offset && offset >= Math.max(data.queued_total, data.active_total)) { offset = 0; return; }
      el("status").textContent = `已更新：${new Date(data.at_ms).toLocaleTimeString()}`;
      el("counts").textContent = `匹配队伍：${data.queued_total} · 进行中的对局（含倒计时、结算）：${data.active_total}`;
      rows("queues", data.queues, q => [q.id, `${q.mode}v${q.mode}`, duration(q.wait_ms), members(q)], "本页没有正在匹配的队伍");
      rows("matches", data.matches, m => [m.id, `${m.mode}v${m.mode}`, m.phase === "battle" ? (m.turn < 0 ? "战斗中 · 回合未知" : `战斗中 · 第 ${m.turn} 回合`) : m.phase === "countdown" ? "开战倒计时" : "结算中", m.phase === "countdown" ? duration(m.countdown_ms) : duration(m.elapsed_ms), m.teams.map((t, i) => `${i === 0 ? "A" : "B"}：${members(t)}`).join(" / ")], "本页没有进行中的对局");
      el("page").textContent = `第 ${offset / 8 + 1} 页`;
      el("prev").disabled = offset === 0;
      el("next").disabled = offset + data.page_size >= Math.max(data.queued_total, data.active_total);
    } catch (err) {
      el("status").textContent = `状态不可用：${err.name === "AbortError" ? "请求超时" : err.message}。已有列表为上次快照。`;
      el("prev").disabled = offset === 0;
    } finally { clearTimeout(timeout); busy = false; }
  }
  el("prev").addEventListener("click", () => { if (!busy) { offset = Math.max(0, offset - 8); refresh(); } });
  el("next").addEventListener("click", () => { if (!busy) { offset += 8; refresh(); } });
  document.addEventListener("visibilitychange", () => { if (!document.hidden) refresh(); });
  refresh(); setInterval(refresh, 3000);
})();
