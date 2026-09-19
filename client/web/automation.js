/* StoneAge automation controls. This module is intentionally independent of
 * the large legacy-compatible index page. It owns only configuration, control
 * state and presentation; all game packets remain behind HTTPTransport and
 * the server-side generation fence. */
(function (root) {
  "use strict";

  const client = root && root.StoneAgeWebClient;
  const app = client && client.app;
  const transport = () => app && app.transport;
  const MODE_QUEST = "quest";
  const MODE_LEVELING = "leveling";
  const MODE_BATTLE = "battle";
  const MODE_MANUAL = "manual";
  const MODE_PAUSED = "paused";
  const ACTIVE_MODES = new Set([MODE_QUEST, MODE_LEVELING, "agent"]);
  /* Auto battle holds the same claim on the character as the executor modes --
     hands off the game UI, battle animations at their fastest -- but it is not
     a resumable task: the server accepts pause and resume for the executor
     modes only, so it is deliberately absent from ACTIVE_MODES. */
  const LOCKED_MODES = new Set([MODE_QUEST, MODE_LEVELING, "agent", MODE_BATTLE]);
  const MAX_CONFIG_BYTES = 64 * 1024;

  const state = {
    control: null,
    savedSpeed: null,
    panelOpen: false,
    pollTimer: 0,
    lastError: "",
    tasks: [],
    taskSession: "",
    taskLoaded: false,
    taskError: "",
    taskRequest: null,
    taskRequestSerial: 0,
    supplies: [],
  };

  function finiteInteger(value, fallback = 0) {
    const number = Number(value);
    return Number.isSafeInteger(number) ? number : fallback;
  }

  function normalizedControl(value) {
    if (!value || typeof value !== "object") return null;
    const control = value.control && typeof value.control === "object" ? value.control : value;
    const generation = finiteInteger(control.generation, 0);
    const mode = String(control.mode || "").toLowerCase();
    if (!generation || !mode) return null;
    return {
      mode,
      generation,
      reason: String(control.reason || ""),
      automationAvailable: value.automation_available !== false && value.automationAvailable !== false,
      automationActive: value.automation_active === true || value.automationActive === true,
      automationMode: String(value.automation_mode || value.automationMode || ""),
      recovery: value.automation_recovery || value.recovery || null,
      recoveryUnavailable: value.automation_recovery_unavailable === true || value.recoveryUnavailable === true,
    };
  }

  function currentControl() {
    return normalizedControl(state.control || transport()?.control || app?.control);
  }

  function publishControl(value) {
    const next = normalizedControl(value);
    if (!next) return null;
    state.control = next;
    if (app) app.control = next;
    updateLock(next);
    renderState(next);
    return next;
  }

  function dispatchControl(value) {
    /* Keep HTTPTransport's fencing token in sync with lifecycle responses.
       A takeover increments the generation; leaving this field stale would
       make the next ordinary browser command reuse the old token and be
       rejected by the bridge. */
    const session = transport();
    if (typeof session?.setControl === "function") {
      const synced = session.setControl(value);
      publishControl(synced || value);
      return;
    }
    publishControl(value);
    try {
      root.dispatchEvent(new CustomEvent("stoneage-control", { detail: value }));
    } catch (_) {
      // WebViews without CustomEvent still get the local state and lock.
    }
  }

  function setBattleSpeed(speed) {
    if (typeof client?.setBattleAnimationSpeed === "function") {
      client.setBattleAnimationSpeed(speed);
      return;
    }
    if (typeof root.setBattleAnimationSpeed === "function") root.setBattleAnimationSpeed(speed);
    else if (app?.systemSettings) app.systemSettings.battleAnimationSpeed = Number(speed) || 1;
  }

  function readBattleSpeed() {
    if (typeof client?.battleAnimationSpeed === "function") return Number(client.battleAnimationSpeed()) || 1;
    if (typeof root.battleAnimationSpeed === "function") return Number(root.battleAnimationSpeed()) || 1;
    return Number(app?.systemSettings?.battleAnimationSpeed) || 1;
  }

  function updateLock(control) {
    if (!root.document?.body) return;
    const locked = LOCKED_MODES.has(String(control?.mode || "").toLowerCase());
    root.document.body.classList.toggle("stoneage-ai-locked", locked);
    root.document.body.dataset.aiControlMode = String(control?.mode || "");
    if (locked) {
      if (state.savedSpeed === null) state.savedSpeed = readBattleSpeed();
      setBattleSpeed(10);
    } else if (state.savedSpeed !== null) {
      setBattleSpeed(state.savedSpeed);
      state.savedSpeed = null;
    }
    const panel = root.document.getElementById("ai-control-panel");
    if (panel) panel.classList.toggle("is-locked", locked);
  }

  function escapeHTML(value) {
    return String(value ?? "").replace(/[&<>"']/g, character => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
    }[character]));
  }

  function stylePanel() {
    if (!root.document || root.document.getElementById("ai-control-style")) return;
    const style = root.document.createElement("style");
    style.id = "ai-control-style";
    style.textContent = `
      #ai-control-toggle { position:fixed; right:12px; top:12px; z-index:3000; padding:6px 10px; color:#ffe9a4; background:#2a2018eF; border:1px solid #b49357; border-radius:3px; font:12px/1.2 sans-serif; }
      #ai-control-panel { position:fixed; right:12px; top:44px; z-index:2999; width:300px; max-width:calc(100vw - 24px); max-height:calc(100vh - 56px); max-height:calc(100dvh - 56px); overflow-y:auto; box-sizing:border-box; padding:10px; color:#f8efd2; background:#1c1712f5; border:1px solid #b49357; border-radius:4px; box-shadow:0 4px 18px #000b; font:12px/1.35 sans-serif; }
      #ai-control-panel[hidden] { display:none; }
      #ai-control-panel h2 { margin:0 0 7px; color:#ffe45c; font-size:14px; }
      #ai-control-panel .ai-row { display:grid; grid-template-columns:88px minmax(0,1fr); gap:5px; align-items:center; margin:4px 0; }
      #ai-control-panel #ai-task-row { grid-template-columns:48px minmax(0,1fr) auto; }
      #ai-control-panel #ai-task-details { overflow-wrap:anywhere; }
      #ai-control-panel input,#ai-control-panel select { min-width:0; width:100%; box-sizing:border-box; }
      #ai-control-panel input[type="checkbox"] { width:auto; }
      #ai-control-panel fieldset { margin:6px 0; padding:5px; border:1px solid #6d5938; min-width:0; }
      #ai-control-panel .ai-actions { display:flex; flex-wrap:wrap; gap:5px; margin-top:8px; }
      #ai-control-panel .ai-actions button { flex:1 1 80px; }
      #ai-control-panel .ai-status { min-height:30px; margin:7px 0 0; color:#d8d0bc; white-space:pre-wrap; }
      #ai-control-panel .ai-status.error { color:#ff8d75; }
      #ai-control-panel .ai-preview { margin-top:6px; padding:5px; color:#d6d0bd; background:#09080688; white-space:pre-wrap; }
      #ai-control-panel .ai-note { color:#b8ad92; font-size:11px; }
      body.stoneage-ai-locked #world, body.stoneage-ai-locked #world-actions, body.stoneage-ai-locked #field-ui, body.stoneage-ai-locked #battle-ui, body.stoneage-ai-locked #battle-target-overlay, body.stoneage-ai-locked #chat-form { pointer-events:none !important; }
      body.stoneage-ai-locked #world-tools, body.stoneage-ai-locked #ai-control-panel, body.stoneage-ai-locked #ai-control-toggle, body.stoneage-ai-locked .advanced-screen { pointer-events:auto !important; }
      body.stoneage-ai-locked .advanced-screen button:not([id$="-close"]):not([id$="-return"]), body.stoneage-ai-locked .advanced-screen input, body.stoneage-ai-locked .advanced-screen select, body.stoneage-ai-locked .advanced-screen textarea { pointer-events:none !important; }
    `;
    root.document.head?.appendChild(style);
  }

  function ensurePanel() {
    if (!root.document || !app || !client) return null;
    stylePanel();
    let toggle = root.document.getElementById("ai-control-toggle");
    let panel = root.document.getElementById("ai-control-panel");
    if (!toggle) {
      toggle = root.document.createElement("button");
      toggle.id = "ai-control-toggle";
      toggle.type = "button";
      toggle.textContent = "自动化";
      toggle.setAttribute("aria-controls", "ai-control-panel");
      toggle.addEventListener("click", () => {
        state.panelOpen = !state.panelOpen;
        const current = root.document.getElementById("ai-control-panel");
        if (current) current.hidden = !state.panelOpen;
        if (state.panelOpen) { refreshPets(); refreshTasks().catch(() => {}); }
      });
      root.document.body?.appendChild(toggle);
    }
    if (!panel) {
      panel = root.document.createElement("aside");
      panel.id = "ai-control-panel";
      panel.hidden = !state.panelOpen;
      panel.setAttribute("aria-label", "自动练级与任务");
      panel.innerHTML = `
        <h2>自动练级与任务</h2>
        <div class="ai-row"><label for="ai-automation-mode">模式</label><select id="ai-automation-mode"><option value="quest">自动任务</option><option value="leveling">自动练级</option><option value="battle">自动战斗</option></select></div>
        <div class="ai-row" id="ai-task-row"><label for="ai-task-id">任务</label><select id="ai-task-id"><option value="">请先加载任务列表</option></select><button id="ai-task-refresh" type="button">刷新</button></div>
        <div id="ai-task-details" class="ai-preview" role="status">任务列表尚未加载。</div>
        <div class="ai-row" id="ai-dependencies-row"><label><input id="ai-include-dependencies" type="checkbox"> 自动完成前置任务（预算包含全链）</label></div>
        <div class="ai-row" id="ai-quest-pet-row"><label for="ai-quest-pet">任务宠物</label><select id="ai-quest-pet"><option value="">未选择（任务有要求时必选）</option></select></div>
        <div class="ai-row" id="ai-target-kind-row"><label for="ai-target-kind">升级对象</label><select id="ai-target-kind"><option value="character">人物</option><option value="pet">宠物</option></select></div>
        <div class="ai-row" id="ai-target-level-row"><label for="ai-target-level">目标等级</label><input id="ai-target-level" type="number" min="1" max="1000" step="1" value="10"></div>
        <div class="ai-row" id="ai-target-pet-row"><label for="ai-target-pet">目标宠物</label><select id="ai-target-pet"><option value="">请先请求宠物状态</option></select></div>
        <div class="ai-row" id="ai-target-policy-row"><label for="ai-target-policy">多个目标</label><select id="ai-target-policy"><option value="all">全部达标</option><option value="any">任一达标</option></select></div>
        <fieldset id="ai-build-section"><legend><label><input id="ai-build-enabled" type="checkbox"> 人物自动加点（可选）</label></legend>
          <div id="ai-build-fields" class="hidden">
            <p class="ai-note">按基础属性比例逐点分配，权重为 0 的属性不加。启动后会使用未分配点数，并保留指定点数。本次方案固定，修改请先接管。</p>
            <div class="ai-row"><label for="ai-build-vital">体力权重</label><input id="ai-build-vital" type="number" min="0" max="100" step="1" value="1"></div>
            <div class="ai-row"><label for="ai-build-strength">腕力权重</label><input id="ai-build-strength" type="number" min="0" max="100" step="1" value="1"></div>
            <div class="ai-row"><label for="ai-build-toughness">耐力权重</label><input id="ai-build-toughness" type="number" min="0" max="100" step="1" value="1"></div>
            <div class="ai-row"><label for="ai-build-dexterity">速度权重</label><input id="ai-build-dexterity" type="number" min="0" max="100" step="1" value="1"></div>
            <div class="ai-row"><label for="ai-build-reserve">保留点数</label><input id="ai-build-reserve" type="number" min="0" max="1000" step="1" value="0"></div>
          </div>
        </fieldset>
        <fieldset id="ai-supply-section"><legend><label><input id="ai-supply-enabled" type="checkbox"> 自动补给（可选）</label></legend>
          <div id="ai-supply-fields" class="hidden">
            <div class="ai-row"><label for="ai-supply-item">补给物品</label><select id="ai-supply-item"><option value="">请先加载已审核补给</option></select></div>
            <div id="ai-supply-details" class="ai-preview" role="status">请选择已审核的补给物品。</div>
            <div class="ai-row"><label for="ai-supply-target">补满数量</label><input id="ai-supply-target" type="number" min="2" max="13" step="1" value="10"></div>
            <div class="ai-row"><label for="ai-supply-reorder">补货阈值</label><input id="ai-supply-reorder" type="number" min="1" max="12" step="1" value="3"></div>
            <p class="ai-note">每次补货保留 2 个背包空位；实际购买数量按短缺计算，并受花费上限限制。</p>
          </div>
        </fieldset>
        <div class="ai-row"><label for="ai-budget-reserve">保留石币</label><input id="ai-budget-reserve" type="number" min="0" step="1" value="0"></div>
        <div class="ai-row"><label for="ai-budget-max">花费上限</label><input id="ai-budget-max" type="number" min="0" step="1" value="0"></div>
        <div class="ai-row"><label for="ai-time-limit">时间上限(秒)</label><input id="ai-time-limit" type="number" min="1" step="1" value="3600"></div>
        <div class="ai-row"><label for="ai-death-limit">失败上限</label><input id="ai-death-limit" type="number" min="0" step="1" value="0"></div>
        <label class="ai-note"><input id="ai-offline-continue" type="checkbox"> 页面关闭后继续托管（需服务端允许）</label>
        <div id="ai-budget-preview" class="ai-preview">预算预览：等待后端知识库核算。费用未知时不可启动。</div>
        <div class="ai-actions"><button id="ai-preview" type="button">预览预算</button><button id="ai-start" type="button">开始</button><button id="ai-pause" type="button">暂停</button><button id="ai-resume" type="button">恢复</button><button id="ai-takeover" type="button">接管</button></div>
        <p id="ai-control-status" class="ai-status" role="status" aria-live="polite"></p>
      `;
      root.document.body?.appendChild(panel);
      const mode = panel.querySelector("#ai-automation-mode");
      mode?.addEventListener("change", renderForm);
      panel.querySelector("#ai-build-enabled")?.addEventListener("change", renderForm);
      panel.querySelector("#ai-supply-enabled")?.addEventListener("change", renderForm);
      ["vital", "strength", "toughness", "dexterity", "reserve"].forEach(key => panel.querySelector(`#ai-build-${key}`)?.addEventListener("input", renderPreview));
      panel.querySelector("#ai-task-refresh")?.addEventListener("click", () => refreshTasks(true).catch(() => {}));
      panel.querySelector("#ai-task-id")?.addEventListener("change", () => { renderTaskDetails(); renderPreview(); });
      panel.querySelector("#ai-target-kind")?.addEventListener("change", renderForm);
      panel.querySelector("#ai-target-pet")?.addEventListener("change", renderPreview);
      panel.querySelector("#ai-quest-pet")?.addEventListener("change", renderPreview);
      panel.querySelector("#ai-supply-item")?.addEventListener("change", () => { renderSupplyDetails(); renderPreview(); renderState(); });
      ["ai-supply-target", "ai-supply-reorder"].forEach(id => panel.querySelector(`#${id}`)?.addEventListener("input", () => { renderSupplyDetails(); renderPreview(); renderState(); }));
      panel.querySelector("#ai-include-dependencies")?.addEventListener("change", renderPreview);
      ["ai-task-id", "ai-target-level", "ai-budget-reserve", "ai-budget-max", "ai-time-limit", "ai-death-limit", "ai-target-policy"].forEach(id => panel.querySelector(`#${id}`)?.addEventListener("input", renderPreview));
      panel.querySelector("#ai-preview")?.addEventListener("click", () => preview().catch(showError));
      panel.querySelector("#ai-start")?.addEventListener("click", () => start().catch(showError));
      panel.querySelector("#ai-pause")?.addEventListener("click", () => pause().catch(showError));
      panel.querySelector("#ai-resume")?.addEventListener("click", () => resume().catch(showError));
      panel.querySelector("#ai-takeover")?.addEventListener("click", () => takeover().catch(showError));
      renderForm();
    }
    return panel;
  }

  function petIdentity(pet, index, all) {
    if (!pet) return null;
    const explicit = pet.id ?? pet.petId ?? pet.serial ?? pet.uid ?? pet.stableId;
    if (explicit !== undefined && String(explicit).trim()) return String(explicit);
    /* A display slot or a mutable status fingerprint cannot identify the same
       pet after a reconnect or level change. Leave it unselectable until the
       server supplies a real serial/UID; the API requires stable identity. */
    return "";
  }

  function refreshPets() {
    const panel = ensurePanel();
    for (const selector of ["#ai-target-pet", "#ai-quest-pet"]) {
      const select = panel?.querySelector(selector);
      if (!select) continue;
      const pets = Array.isArray(app?.petSlots) ? app.petSlots : [];
      const previous = select.value;
      select.replaceChildren();
      if (selector === "#ai-quest-pet") {
        const option = root.document.createElement("option");
        option.value = "";
        option.textContent = "未选择（任务有要求时必选）";
        select.appendChild(option);
      }
      const occupied = pets.map((pet, index) => ({ pet, index })).filter(item => item.pet);
      if (!occupied.length) {
        const option = root.document.createElement("option");
        option.value = "";
        option.textContent = "暂无已同步宠物";
        select.appendChild(option);
        continue;
      }
      occupied.forEach(({ pet, index }) => {
        const id = petIdentity(pet, index, pets);
        const option = root.document.createElement("option");
        option.value = id;
        option.disabled = !id;
        option.textContent = `${pet.freeName || pet.userPetName || pet.name || `宠物 ${index + 1}`} · Lv${pet.level ?? "?"}${id ? "" : " · 缺少稳定身份"}`;
        option.dataset.slotAtSelection = String(index);
        option.dataset.petIdentity = id;
        select.appendChild(option);
      });
      if ([...select.options].some(option => option.value === previous)) select.value = previous;
    }
    renderPreview();
  }

  function supplyOffer(alias) {
    const value = String(alias || "").trim();
    if (!value) return null;
    return state.supplies.find(offer => String(offer?.alias || "").trim() === value) || null;
  }

  function supplyUnitPrice(offer) {
    const value = Number(offer?.unit_price ?? offer?.unitPrice);
    return Number.isSafeInteger(value) && value >= 0 ? value : null;
  }

  function supplyDisplayName(offer) {
    const alias = String(offer?.alias || "").trim();
    const name = String(offer?.name || "").trim() || alias;
    const npc = String(offer?.npc || "").trim();
    return npc && !name.includes(npc) ? `${name}（${npc}）` : name;
  }

  function supplyCount(panel, id) {
    return Number(panel?.querySelector(`#${id}`)?.value);
  }

  function renderSupplyDetails() {
    const panel = root.document?.getElementById("ai-control-panel");
    const node = panel?.querySelector("#ai-supply-details");
    if (!node) return;
    const item = panel.querySelector("#ai-supply-item")?.value;
    const offer = supplyOffer(item);
    if (!offer) {
      if (state.taskRequest && !state.taskLoaded) node.textContent = "正在加载补给目录…";
      else if (state.taskLoaded && !state.supplies.length) node.textContent = "当前没有已审核补给，无法启用自动补给。";
      else node.textContent = state.supplies.length ? "请选择已审核的补给物品；每次补货保留 2 个背包空位。" : "补给目录尚未加载；请先刷新任务目录。";
      return;
    }
    const unitPrice = supplyUnitPrice(offer);
    const target = supplyCount(panel, "ai-supply-target");
    const reorderInput = panel.querySelector("#ai-supply-reorder");
    const reorderMaximum = Number.isSafeInteger(target) && target >= 2 ? target - 1 : 1;
    if (reorderInput) {
      reorderInput.max = String(reorderMaximum);
      reorderInput.setAttribute?.("max", String(reorderMaximum));
    }
    const maximum = unitPrice !== null && Number.isSafeInteger(target) && target >= 0 ? target * unitPrice : null;
    const maximumText = maximum !== null && Number.isSafeInteger(maximum) ? `${maximum} 石币` : "待填写整数";
    const unitText = unitPrice === null ? "未知" : `${unitPrice} 石币`;
    node.textContent = `补给：${supplyDisplayName(offer)}\n单价：${unitText} · 补满最高费用：${maximumText}\n每次补货保留 2 个背包空位；实际购买数量按短缺计算，并受花费上限限制。`;
  }

  function supplyConfigurationReady(panel) {
    if (!panel?.querySelector("#ai-supply-enabled")?.checked) return true;
    const item = String(panel.querySelector("#ai-supply-item")?.value || "").trim();
    if (!supplyOffer(item)) return false;
    const target = Number(panel.querySelector("#ai-supply-target")?.value);
    const reorder = Number(panel.querySelector("#ai-supply-reorder")?.value);
    return Number.isSafeInteger(target) && target >= 2 && target <= 13 &&
      Number.isSafeInteger(reorder) && reorder >= 1 && reorder < target;
  }

  function panel_mode() {
    return ensurePanel()?.querySelector("#ai-automation-mode")?.value || MODE_QUEST;
  }

  function selectedConfig() {
    const panel = ensurePanel();
    if (!panel) throw new Error("自动化面板不可用");
    const mode = String(panel.querySelector("#ai-automation-mode")?.value || MODE_QUEST);
    const budget = {
      reserve: Math.max(0, finiteInteger(panel.querySelector("#ai-budget-reserve")?.value, 0)),
      maximum_spend: Math.max(0, finiteInteger(panel.querySelector("#ai-budget-max")?.value, 0)),
    };
    const config = {
      mode,
      task_id: String(panel.querySelector("#ai-task-id")?.value || "").trim(),
      target_policy: String(panel.querySelector("#ai-target-policy")?.value || "all"),
      include_dependencies: mode === MODE_QUEST && Boolean(panel.querySelector("#ai-include-dependencies")?.checked),
      selected_pet_id: mode === MODE_QUEST ? String(panel.querySelector("#ai-quest-pet")?.value || "") : "",
      targets: [],
      budget,
      maximum_seconds: Math.max(0, finiteInteger(panel.querySelector("#ai-time-limit")?.value, 0)),
      maximum_deaths: Math.max(0, finiteInteger(panel.querySelector("#ai-death-limit")?.value, 0)),
      offline_continue: Boolean(panel.querySelector("#ai-offline-continue")?.checked),
    };
    if (mode === MODE_LEVELING) {
      const kind = String(panel.querySelector("#ai-target-kind")?.value || "character");
      const level = Math.max(0, finiteInteger(panel.querySelector("#ai-target-level")?.value, 0));
      const pet = panel.querySelector("#ai-target-pet");
      const target = { kind, level };
      if (kind === "pet") {
        target.id = String(pet?.value || "");
      } else {
        target.id = String(app?.character || app?.pc?.name || "");
      }
      config.targets.push(target);
      if (panel.querySelector("#ai-build-enabled")?.checked) {
        const read = (key, max) => {
          const value = Number(panel.querySelector(`#ai-build-${key}`)?.value);
          if (!Number.isSafeInteger(value) || value < 0 || value > max) throw new Error(`加点设置必须填写 0–${max} 的整数`);
          return value;
        };
        const weights = Object.fromEntries(["vital", "strength", "toughness", "dexterity"].map(key => [key, read(key, 100)]));
        if (!Object.values(weights).some(value => value > 0)) throw new Error("请为至少一项人物属性设置正权重");
        config.character_build = { weights, reserve_points: read("reserve", 1000) };
      }
      if (panel.querySelector("#ai-supply-enabled")?.checked) {
        const item = String(panel.querySelector("#ai-supply-item")?.value || "").trim();
        if (!item || !supplyOffer(item)) throw new Error("请选择目录中的已审核补给物品");
        const readSupply = (id, label, min, max) => {
          const value = Number(panel.querySelector(`#${id}`)?.value);
          if (!Number.isSafeInteger(value) || value < min || value > max) throw new Error(`${label}必须填写 ${min}–${max} 的整数`);
          return value;
        };
        const targetCount = readSupply("ai-supply-target", "补满数量", 2, 13);
        const reorderCount = readSupply("ai-supply-reorder", "补货阈值", 1, 12);
        if (reorderCount >= targetCount) throw new Error("补货阈值必须小于补满数量");
        config.supply = { item, target_count: targetCount, reorder_count: reorderCount };
      }
    }
    return config;
  }

  function currentGeneration() {
    const control = currentControl();
    if (!control?.generation) throw new Error("尚未取得控制代际，请刷新状态");
    return control.generation;
  }

  function jsonBody(config) {
    const body = JSON.stringify({ generation: currentGeneration(), ...config });
    if (body.length > MAX_CONFIG_BYTES) throw new Error("自动化配置过大");
    return body;
  }

  async function controlRequest(path, options = {}, syncControl = true) {
    const session = transport();
    if (!session?.id || session.closed) throw new Error("游戏会话已关闭");
    const sessionID = session.id;
    const response = await fetch(`${session.base}/api/sessions/${encodeURIComponent(sessionID)}/${path}`, {
      ...options,
      headers: { Accept: "application/json", "Content-Type": "application/json", ...(options.headers || {}) },
      cache: "no-store",
    });
    let data = null;
    let errorText = "";
    if (response.ok) {
      try { data = await response.json(); } catch (_) { /* optional empty success body */ }
    } else {
      /* The bridge uses http.Error for failures, so preserve its useful
         Chinese message instead of reducing every validation error to a bare
         HTTP status. */
      errorText = await response.text();
      try { data = JSON.parse(errorText); } catch (_) { /* plain-text error */ }
    }
    if (transport() !== session || session.id !== sessionID || session.closed) throw new Error("游戏会话已变化，请刷新自动化状态");
    if (!response.ok) {
      const message = data?.error || data?.message || errorText.trim() || `HTTP ${response.status}`;
      const error = new Error(message);
      error.status = response.status;
      throw error;
    }
    if (data && syncControl) dispatchControl(data);
    return data;
  }

  function taskSessionKey() {
    const session = transport();
    return session?.id && !session.closed ? `${session.base}|${session.id}` : "";
  }

  function selectedTask() {
    if (!state.taskLoaded || state.taskSession !== taskSessionKey()) return null;
    const id = root.document?.getElementById("ai-control-panel")?.querySelector("#ai-task-id")?.value;
    return state.tasks.find(task => task.id === id) || null;
  }

  function renderTaskDetails() {
    const panel = root.document?.getElementById("ai-control-panel");
    const node = panel?.querySelector("#ai-task-details");
    if (!node) return;
    const task = selectedTask();
    if (state.taskError) node.textContent = `任务列表不可用：${state.taskError}`;
    else if (!state.taskLoaded) node.textContent = state.taskRequest ? "正在加载任务列表…" : "请连接游戏后加载任务列表。";
    else if (!task) node.textContent = state.tasks.length ? "请选择任务查看要求；启动前仍需预检。" : "当前知识库尚未提供任务。";
    else {
      const lines = [task.description || task.name];
      if (task.requirements?.length) lines.push(`前置要求：${task.requirements.join("；")}`);
      if (task.dependencies?.length) lines.push(`前置任务：${task.dependencies.map(item => item.name || item.id).join("、")}`);
      if (task.requires_pet) lines.push("请绑定自己的任务宠物；执行期间保留同一只宠物。");
      if (task.preparation_notes) lines.push(`准备说明：${task.preparation_notes}`);
      if (task.review_blockers?.length) lines.push(`暂不可执行：${task.review_blockers.join("；")}`);
      else lines.push("请预检当前角色、宠物和资金；启动时会再次核验。");
      node.textContent = lines.filter(Boolean).join("\n");
    }
    renderState();
  }

  function renderTaskCatalog() {
    const select = root.document?.getElementById("ai-control-panel")?.querySelector("#ai-task-id");
    if (select) {
      const previous = select.value;
      select.replaceChildren();
      const empty = root.document.createElement("option");
      empty.value = "";
      empty.textContent = state.taskLoaded ? (state.tasks.length ? "请选择任务" : "暂无任务") : "任务列表未加载";
      select.appendChild(empty);
      for (const task of state.tasks) {
        const option = root.document.createElement("option");
        option.value = task.id;
        option.textContent = `${task.name || task.id}${task.review_blockers?.length ? "（待核验）" : ""}`;
        select.appendChild(option);
      }
      if (state.tasks.some(task => task.id === previous)) select.value = previous;
      renderTaskDetails();
    }
    renderSupplyCatalog();
  }

  function renderSupplyCatalog() {
    const select = root.document?.getElementById("ai-control-panel")?.querySelector("#ai-supply-item");
    if (!select) return;
    const previous = select.value;
    select.replaceChildren();
    const empty = root.document.createElement("option");
    empty.value = "";
    empty.textContent = state.supplies.length ? "请选择已审核补给" : "暂无已审核补给";
    select.appendChild(empty);
    for (const offer of state.supplies) {
      const alias = String(offer?.alias || "").trim();
      if (!alias) continue;
      const option = root.document.createElement("option");
      option.value = alias;
      const unitPrice = supplyUnitPrice(offer);
      const displayName = supplyDisplayName(offer);
      option.textContent = unitPrice === null ? displayName : `${displayName} · 单价 ${unitPrice}`;
      option.dataset.alias = alias;
      option.dataset.unitPrice = unitPrice === null ? "" : String(unitPrice);
      select.appendChild(option);
    }
    if (state.supplies.some(offer => String(offer?.alias || "").trim() === previous)) select.value = previous;
    renderSupplyDetails();
    renderState();
  }

  async function refreshTasks(force = false) {
    const key = taskSessionKey();
    if (state.taskSession !== key) {
      state.taskRequestSerial++;
      state.tasks = [];
      state.supplies = [];
      state.taskLoaded = false;
      state.taskError = "";
      state.taskRequest = null;
      state.taskSession = key;
      renderTaskCatalog();
    }
    if (!key) return null;
    if (state.taskRequest) return state.taskRequest;
    if (state.taskLoaded && !force) return state.tasks;
    const serial = ++state.taskRequestSerial;
    state.taskLoaded = false;
    state.taskError = "";
    const request = controlRequest("automation/tasks", { method: "GET" }, false);
    state.taskRequest = request;
    renderTaskDetails();
    try {
      const data = await request;
      if (serial !== state.taskRequestSerial || key !== taskSessionKey()) return null;
      if (!data || !Array.isArray(data.tasks)) throw new Error("任务列表响应无效");
      state.tasks = data.tasks;
      state.supplies = Array.isArray(data.supplies) ? data.supplies.filter(offer => String(offer?.alias || "").trim()) : [];
      state.taskLoaded = true;
      renderTaskCatalog();
      return state.tasks;
    } catch (error) {
      if (serial === state.taskRequestSerial && key === taskSessionKey()) {
        state.tasks = [];
        state.supplies = [];
        state.taskError = String(error?.message || error);
        renderTaskCatalog();
      }
      return null;
    } finally {
      if (serial === state.taskRequestSerial) state.taskRequest = null;
    }
  }

  async function refreshControl() {
    const session = transport();
    if (!session?.id || session.closed) return currentControl();
    try {
      const data = await controlRequest("control", { method: "GET", headers: { "Content-Type": "" } });
      return normalizedControl(data) || currentControl();
    } catch (error) {
      state.lastError = String(error?.message || error);
      return currentControl();
    }
  }

  async function preview() {
    const panel = ensurePanel();
    const node = panel?.querySelector("#ai-budget-preview");
    const executorAvailable = currentControl()?.automationAvailable !== false && transport()?.automationAvailable !== false;
    if (!executorAvailable) {
      if (node) node.textContent = "预算预览不可用：服务端尚未启用自动化。费用未知，不能启动。";
      return null;
    }
    const data = await controlRequest("automation/preview", { method: "POST", body: jsonBody(selectedConfig()) });
    if (node) {
      const budget = data?.budget || {};
      const problems = Array.isArray(data?.problems) ? data.problems : [];
      node.textContent = `预算：最低 ${budget.minimum ?? "?"} · 预计 ${budget.expected_low ?? "?"}–${budget.expected_high ?? "?"} · 保留 ${budget.reserve ?? "?"} · 上限 ${budget.maximum_spend ?? "?"}${problems.length ? `\n${problems.join("；")}` : data?.ready ? "\n前置条件已满足" : "\n尚未满足启动条件"}`;
      if (Array.isArray(data?.task_order) && data.task_order.length) node.textContent += `\n任务顺序：${data.task_order.join(" → ")}\n预算按完整任务链保守核算；已完成节点将根据当前状态跳过。`;
    }
    return data;
  }

  async function start() {
    const control = await refreshControl();
    if (!control || control.mode !== MODE_MANUAL) throw new Error("请先处于人工控制状态");
    const mode = String(panel_mode());
    if (mode === MODE_BATTLE) {
      /* Auto battle carries no task, budget or targets: it answers whatever
         turn the character is in. The server reads only the generation. */
      const data = await controlRequest("battle-auto", { method: "POST", body: JSON.stringify({ generation: currentGeneration() }) });
      setStatus("自动战斗中：血量低时先治疗，否则按顺序攻击，不会逃跑。点「接管」随时停止。", false);
      return data;
    }
    const config = selectedConfig();
    if (config.mode === MODE_QUEST) {
      const task = selectedTask();
      if (!task || task.id !== config.task_id) throw new Error("请加载任务列表并选择任务");
      if (task.review_blockers?.length) throw new Error(task.review_blockers.join("；"));
      if (task.requires_pet && !config.selected_pet_id) throw new Error("请选择已同步的任务宠物");
    }
    if (config.mode === MODE_LEVELING && config.targets.some(target => target.kind === "pet" && !target.id)) throw new Error("目标宠物必须绑定稳定身份");
    const data = await controlRequest("automation/start", { method: "POST", body: jsonBody(config) });
    setStatus("自动化已启动，游戏操作已锁定。", false);
    return data;
  }

  async function pause() {
    const control = currentControl();
    if (!control || !ACTIVE_MODES.has(control.mode)) throw new Error("当前没有可暂停的自动化");
    const data = await controlRequest("pause", { method: "POST", body: JSON.stringify({ generation: control.generation, reason: "玩家暂停" }) });
    setStatus("自动化已暂停，可以查看并操作游戏。", false);
    return data;
  }

  async function resume() {
    const control = currentControl();
    if (!control || (control.mode !== MODE_PAUSED && !(control.mode === MODE_MANUAL && control.recovery))) throw new Error("当前没有暂停的自动化");
    const data = await controlRequest("resume", { method: "POST", body: JSON.stringify({ generation: control.generation, mode: control.recovery?.mode || control.automationMode || MODE_LEVELING, recovery_handle: control.recovery?.handle || undefined, reason: "玩家恢复" }) });
    setStatus("自动化已恢复，游戏操作重新锁定。", false);
    return data;
  }

  async function takeover() {
    const control = currentControl();
    const data = await controlRequest("takeover", { method: "POST", body: JSON.stringify({ reason: "玩家接管", generation:control?.generation, recovery_handle:control?.recovery?.handle || undefined }) });
    setStatus("已接管游戏控制权。", false);
    return data;
  }

  function setStatus(message, error = false) {
    if (!error) state.lastError = "";
    const node = root.document?.getElementById("ai-control-status");
    if (node) {
      node.textContent = String(message || "");
      node.classList.toggle("error", Boolean(error));
    }
  }

  function showError(error) {
    const message = String(error?.message || error || "自动化操作失败");
    state.lastError = message;
    setStatus(message, true);
  }

  function renderState(control = currentControl()) {
    if (!control) return;
    const panel = root.document?.getElementById("ai-control-panel");
    const status = panel?.querySelector("#ai-control-status");
    if (!panel || !status) return;
    const modeLabel = ({ manual: "人工", paused: "已暂停", quest: "自动任务", leveling: "自动练级", battle: "自动战斗", agent: "AI 玩家" })[control.mode] || control.mode;
    const detail = control.reason ? `（${control.reason}）` : "";
    if (!state.lastError) status.textContent = `控制：${modeLabel}${detail}` + (control.recovery ? "\n发现断线前的自动任务，可恢复或取消旧任务。" : control.recoveryUnavailable ? "\n暂时无法读取断线任务，请稍后刷新。" : "");
    status.classList.toggle("error", Boolean(state.lastError));
    panel.querySelector("#ai-build-section")?.toggleAttribute("disabled", control.mode !== MODE_MANUAL);
    panel.querySelector("#ai-supply-section")?.toggleAttribute("disabled", control.mode !== MODE_MANUAL);
    panel.querySelector("#ai-include-dependencies")?.toggleAttribute("disabled", control.mode !== MODE_MANUAL);
    const questMode = panel.querySelector("#ai-automation-mode")?.value === MODE_QUEST;
    const levelingMode = panel.querySelector("#ai-automation-mode")?.value === MODE_LEVELING;
    const battleMode = panel.querySelector("#ai-automation-mode")?.value === MODE_BATTLE;
    const task = questMode ? selectedTask() : null;
    /* Auto battle answers turns on its own; it needs no task catalog, no
       planner and no budget, so a bridge that reports automation_available
       false must not make it un-startable. */
    const needsExecutor = !battleMode && control.automationAvailable === false;
    panel.querySelector("#ai-start")?.toggleAttribute("disabled", control.mode !== MODE_MANUAL || Boolean(control.recovery) || control.recoveryUnavailable || needsExecutor || (questMode && (!task || Boolean(task.review_blockers?.length))) || (levelingMode && !supplyConfigurationReady(panel)));
    panel.querySelector("#ai-pause")?.toggleAttribute("disabled", !ACTIVE_MODES.has(control.mode));
    panel.querySelector("#ai-resume")?.toggleAttribute("disabled", control.mode !== MODE_PAUSED && !(control.mode === MODE_MANUAL && control.recovery));
    panel.querySelector("#ai-takeover")?.toggleAttribute("disabled", control.mode === MODE_MANUAL && !control.recovery);
    const takeoverButton=panel.querySelector("#ai-takeover"); if(takeoverButton) takeoverButton.textContent=control.recovery ? "取消旧任务" : "接管";
  }

  function renderForm() {
    const panel = ensurePanel();
    if (!panel) return;
    const mode = String(panel.querySelector("#ai-automation-mode")?.value || MODE_QUEST);
    panel.querySelector("#ai-task-row")?.classList.toggle("hidden", mode !== MODE_QUEST);
    panel.querySelector("#ai-task-details")?.classList.toggle("hidden", mode !== MODE_QUEST);
    panel.querySelector("#ai-build-section")?.classList.toggle("hidden", mode !== MODE_LEVELING);
    panel.querySelector("#ai-build-fields")?.classList.toggle("hidden", !panel.querySelector("#ai-build-enabled")?.checked);
    panel.querySelector("#ai-supply-section")?.classList.toggle("hidden", mode !== MODE_LEVELING);
    panel.querySelector("#ai-supply-fields")?.classList.toggle("hidden", !panel.querySelector("#ai-supply-enabled")?.checked);
    panel.querySelector("#ai-quest-pet-row")?.classList.toggle("hidden", mode !== MODE_QUEST);
    panel.querySelector("#ai-dependencies-row")?.classList.toggle("hidden", mode !== MODE_QUEST);
    ["ai-target-kind-row", "ai-target-level-row", "ai-target-pet-row", "ai-target-policy-row"].forEach(id => panel.querySelector(`#${id}`)?.classList.toggle("hidden", mode !== MODE_LEVELING || (id === "ai-target-pet-row" && panel.querySelector("#ai-target-kind")?.value !== "pet")));
    refreshPets();
    refreshTasks().catch(() => {});
    renderSupplyDetails();
    renderPreview();
    renderState();
  }

  function renderPreview() {
    const panel = root.document?.getElementById("ai-control-panel");
    const node = panel?.querySelector("#ai-budget-preview");
    if (!node) return;
    let config;
    try { config = selectedConfig(); }
    catch (error) { node.textContent = String(error?.message || error); return; }
    const hasLimit = config.budget.maximum_spend > 0;
    node.textContent = hasLimit ? `本地配置：保留 ${config.budget.reserve} · 花费上限 ${config.budget.maximum_spend}\n实际费用仍需后端知识库验证。` : "预算预览：未设置有效花费上限；费用未知时不可启动。";
  }

  function updateFromTransport(value) {
    if (value) dispatchControl(value);
    else publishControl(currentControl());
    refreshPets();
  }

  function lockGameplay(event) {
    const control = currentControl();
    if (!control || !LOCKED_MODES.has(control.mode)) return;
    const target = event.target;
    if (!(target instanceof Element)) return;
    if (target.closest("#ai-control-panel,#ai-control-toggle,#world-tools,.advanced-screen [id$='-close'],.advanced-screen [id$='-return']")) return;
    event.preventDefault();
    event.stopImmediatePropagation();
  }

  const api = {
    state,
    currentControl,
    publishControl,
    refreshControl,
    refreshTasks,
    refreshPets,
    selectedConfig,
    preview,
    start,
    pause,
    resume,
    takeover,
    updateFromTransport,
  };
  if (root) root.StoneAgeAutomation = api;

  if (!root.document || !app || !client) return;
  ensurePanel();
  root.document.addEventListener("click", lockGameplay, true);
  root.document.addEventListener("pointerdown", lockGameplay, true);
  /* HTTPTransport emits this event after connect/events/control requests. Do
     not re-emit it here: updateFromTransport() intentionally emits events for
     external callers, while this listener only consumes transport updates. */
  root.addEventListener?.("stoneage-control", event => {
    if (event?.detail) publishControl(event.detail);
    refreshPets();
  });
  updateFromTransport(currentControl());
  state.pollTimer = root.setInterval?.(() => {
    refreshControl().catch(() => {});
    if (state.panelOpen && state.taskSession !== taskSessionKey()) refreshTasks().catch(() => {});
    refreshPets();
  }, 2000) || 0;
})(typeof window !== "undefined" ? window : globalThis);
