"use strict";

(function () {
  const modelRoot = document.getElementById("ai-model-admin");
  const profileRoot = document.getElementById("ai-profile-admin");

  function csrf(root) {
    return root ? root.dataset.csrf || "" : "";
  }

  async function api(root, path, method, body) {
    const headers = { "X-CSRF-Token": csrf(root), "Accept": "application/json" };
    const options = { method: method || "GET", headers: headers, credentials: "same-origin" };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      options.body = JSON.stringify(body);
    }
    const response = await fetch(path, options);
    let value = {};
    try { value = await response.json(); } catch (_) { value = {}; }
    if (!response.ok) {
      throw new Error(value.error || "请求失败");
    }
    return value;
  }

  function message(root, text, error) {
    const element = root && root.querySelector("[id$='-message']");
    if (!element) return;
    element.hidden = !text;
    element.textContent = text || "";
    element.classList.toggle("error", !!error);
    element.classList.toggle("success", !error);
  }

  function value(form, name) {
    const control = form.elements.namedItem(name);
    return control ? control.value.trim() : "";
  }

  function checked(form, name) {
    const control = form.elements.namedItem(name);
    return !!control && control.checked;
  }

  function integer(form, name) {
    const parsed = Number.parseInt(value(form, name), 10);
    return Number.isFinite(parsed) ? parsed : 0;
  }

  function show(element) { if (element) element.hidden = false; }
  function hide(element) { if (element) element.hidden = true; }

  function initialConfig(form, pets) {
    const mode = value(form, "initial_mode") || "birth";
    const mount = checked(form, "initial_mount");
    const number = function(name, min, max) {
      const raw = value(form, name), n = Number(raw);
      if (!raw || !Number.isInteger(n) || n < min || n > max) throw new Error("初始状态数值超出范围");
      return n;
    };
    if (mode === "birth") return {mode, mount};
    if (mode === "custom") {
      if (pets.length > 5) throw new Error("自定义初始宠物最多 5 只");
      if (mount && !pets.length) throw new Error("默认骑乘需要至少一只初始宠物");
      const weights = {};
      ["vital", "strength", "toughness", "dexterity"].forEach(k => {weights[k] = number("initial_" + k, 0, 100);});
      if (!Object.values(weights).some(n => n > 0)) throw new Error("至少一项初始配点权重大于 0");
      const customPets = pets.map(p => {
        const level = Number(p.level);
        if (!Number.isInteger(level) || level < 1 || level > 140) throw new Error("请为每只自定义宠物设置 1–140 级");
        return {template_id:p.template_id, level};
      });
      return {mode, mount, character_level:number("initial_level",1,140), hometown:number("initial_hometown",0,3), weights, pets:customPets};
    }
    if (mode !== "random") throw new Error("初始状态模式无效");
    const range = function(prefix, min, max) {
      const r = {min:number(prefix + "_min",min,max),max:number(prefix + "_max",min,max)};
      if (r.min > r.max) throw new Error("初始状态下限不能高于上限");
      return r;
    };
    const rules = {character_level:range("initial_level",1,140),pet_level:range("initial_pet",1,140),pet_count:range("initial_count",0,5),pet_templates:Array.from(new Set(pets.map(p => p.template_id))),hometowns:[0,1,2,3]};
    if (mount && rules.pet_count.min < 1) throw new Error("默认骑乘需要至少一只初始宠物");
    if (rules.pet_count.max > 0 && !rules.pet_templates.length) throw new Error("请选择随机宠物候选，或将宠物数量设为 0");
    return {mode, mount, random:rules};
  }
  function initialRidingText(actual) {
    if (!actual) return "待确认";
    const slot = actual.ride_pet_slot;
    if (slot === -1) return "未骑乘";
    if (Number.isInteger(slot) && slot >= 0 && slot < 5) return "已骑乘第 " + (slot + 1) + " 只宠物";
    return "待确认";
  }
  window.StoneAgeAIInitial = {config:initialConfig, ridingText:initialRidingText};

  function initModels() {
    if (!modelRoot) return;
    const editor = document.getElementById("ai-model-editor");
    const form = document.getElementById("ai-model-form");
    const title = document.getElementById("ai-model-editor-title");
    const canWrite = modelRoot.dataset.canWrite === "true";
    const connectionTestAvailable = modelRoot.dataset.connectionTestAvailable === "true";
    const connectionTestUnavailableMessage = "当前运行环境未接入模型连接测试，可通过启动 AI 玩家查看运行结果。";
    const newButton = document.getElementById("ai-model-new");
    const preset = form && form.elements.namedItem("preset");
    const catalog = {
      backend: modelRoot.dataset.catalogBackend || "",
      provider: modelRoot.dataset.catalogProvider || "",
      baseURL: modelRoot.dataset.catalogBaseUrl || "",
      model: modelRoot.dataset.catalogModel || "",
      defaultReasoning: modelRoot.dataset.catalogDefaultReasoning || "",
      reasoningLevels: ["none", "minimal", "low", "medium", "high", "xhigh", "max"]
    };

    function presetOption(id) {
      if (!preset) return null;
      return Array.from(preset.options).find(function (option) { return option.value === id; }) || null;
    }

    function selectedPreset() {
      return preset ? preset.options[preset.selectedIndex] : null;
    }

    function reasoningLevels(option, fallback) {
      const raw = option && option.dataset.reasoningLevels ? option.dataset.reasoningLevels : "";
      const levels = raw.split(",").map(function (level) { return level.trim(); }).filter(Boolean);
      return levels.length ? levels : (fallback || catalog.reasoningLevels);
    }

    function updateReasoningOptions(option, selected) {
      const control = form && form.elements.namedItem("reasoning_effort");
      if (!control) return;
      const levels = reasoningLevels(option);
      control.textContent = "";
      const unspecified = document.createElement("option");
      unspecified.value = "";
      unspecified.textContent = "不指定";
      control.appendChild(unspecified);
      levels.forEach(function (level) {
        const item = document.createElement("option");
        item.value = level;
        item.textContent = level;
        control.appendChild(item);
      });
      const requested = selected || (option && option.dataset.defaultReasoning) || "";
      control.value = Array.from(control.options).some(function (item) { return item.value === requested; }) ? requested : "";
    }

    function applyPreset(id) {
      const option = presetOption(id) || selectedPreset();
      if (!option || !form) return;
      form.elements.namedItem("provider").value = option.dataset.provider || "";
      form.elements.namedItem("base_url").value = option.dataset.baseUrl || "";
      form.elements.namedItem("model").value = option.dataset.model || "";
      form.elements.namedItem("wire_api").value = option.dataset.wireApi || "responses";
      updateReasoningOptions(option);
    }

    function modelMatchesPreset(config, option) {
      if (!option || option.value === "custom") return false;
      return (option.dataset.provider || "") === (config.provider || "") &&
        (option.dataset.baseUrl || "").replace(/\/+$/, "") === (config.base_url || "").replace(/\/+$/, "") &&
        (option.dataset.model || "") === (config.model || "") &&
        (option.dataset.wireApi || "responses") === (config.wire_api || "responses");
    }

    function reset() {
      if (!form) return;
      form.reset();
      form.elements.namedItem("id").value = "";
      form.elements.namedItem("expected_version").value = "";
      form.elements.namedItem("backend").value = catalog.backend;
      if (preset && presetOption("deepseek")) {
        preset.value = "deepseek";
        applyPreset("deepseek");
      } else {
        updateReasoningOptions(null, "");
      }
      form.elements.namedItem("timeout_seconds").value = "120";
      form.elements.namedItem("max_output_tokens").value = "4096";
      form.elements.namedItem("daily_token_budget").value = "100000";
      hide(editor);
    }

    function edit(config) {
      if (!form || !editor) return;
      form.elements.namedItem("id").value = config.id || "";
      form.elements.namedItem("expected_version").value = config.version || "";
      form.elements.namedItem("name").value = config.name || "";
      form.elements.namedItem("backend").value = catalog.backend || config.backend || "";
      if (config.id) {
        form.elements.namedItem("provider").value = config.provider || "";
        form.elements.namedItem("base_url").value = config.base_url || "";
        form.elements.namedItem("model").value = config.model || "";
        form.elements.namedItem("wire_api").value = config.wire_api || "responses";
        const matching = Array.from(preset ? preset.options : []).find(function (option) { return modelMatchesPreset(config, option); });
        if (preset) preset.value = matching ? matching.value : "custom";
        updateReasoningOptions(matching || selectedPreset(), config.reasoning_effort || "");
      }
      form.elements.namedItem("timeout_seconds").value = config.timeout_seconds || 120;
      form.elements.namedItem("max_output_tokens").value = config.max_output_tokens || 4096;
      form.elements.namedItem("daily_token_budget").value = config.daily_token_budget || 0;
      form.elements.namedItem("api_key").value = "";
      form.elements.namedItem("clear_key").checked = false;
      form.elements.namedItem("default").checked = !!config.default;
      title.textContent = config.id ? "编辑模型" : "新增模型";
      show(editor);
      editor.scrollIntoView({ behavior: "smooth", block: "start" });
      form.elements.namedItem("name").focus();
    }

    if (preset && canWrite) preset.addEventListener("change", function () { applyPreset(preset.value); });
    if (newButton && canWrite) newButton.addEventListener("click", function () {
      reset();
      edit({});
    });
    const cancel = document.getElementById("ai-model-cancel");
    if (cancel) cancel.addEventListener("click", reset);
    const applyTemplate = document.getElementById("ai-apply-deepseek");
    if (applyTemplate && canWrite) applyTemplate.addEventListener("click", function () {
      reset();
      edit({backend: catalog.backend, provider: catalog.provider, base_url: catalog.baseURL, model: catalog.model, reasoning_effort: catalog.defaultReasoning});
    });

    document.querySelectorAll(".ai-edit-model").forEach(function (button) {
      button.addEventListener("click", async function () {
        try {
          const value = await api(modelRoot, "/api/ai/models/" + encodeURIComponent(button.dataset.id), "GET");
          edit(value.model || {});
        } catch (error) { message(modelRoot, error.message, true); }
      });
    });

    if (form && canWrite) form.addEventListener("submit", async function (event) {
      event.preventDefault();
      const id = value(form, "id");
      const body = {
        name: value(form, "name"), backend: value(form, "backend"), provider: value(form, "provider"),
        base_url: value(form, "base_url"), model: value(form, "model"),
        wire_api: value(form, "wire_api"),
        reasoning_effort: value(form, "reasoning_effort"),
        timeout_seconds: integer(form, "timeout_seconds"), max_output_tokens: integer(form, "max_output_tokens"),
        daily_token_budget: integer(form, "daily_token_budget"), default: checked(form, "default")
      };
      const key = value(form, "api_key");
      if (key) body.api_key = key;
      if (checked(form, "clear_key")) body.clear_key = true;
      if (id) body.expected_version = integer(form, "expected_version");
      try {
        await api(modelRoot, id ? "/api/ai/models/" + encodeURIComponent(id) : "/api/ai/models", id ? "PATCH" : "POST", body);
        window.location.reload();
      } catch (error) { message(modelRoot, error.message, true); }
    });

    function prepareConnectionTestButton(button) {
      if (!connectionTestAvailable) {
        button.disabled = true;
        button.setAttribute("aria-disabled", "true");
        button.title = connectionTestUnavailableMessage;
      }
    }

    document.querySelectorAll(".ai-test-model").forEach(function (button) {
      prepareConnectionTestButton(button);
      button.addEventListener("click", async function () {
        if (!connectionTestAvailable || button.disabled) return;
        button.disabled = true;
        message(modelRoot, "正在请求 Codex runtime 测试连接…", false);
        try {
          await api(modelRoot, "/api/ai/models/" + encodeURIComponent(button.dataset.id) + "/test", "POST", {});
          message(modelRoot, "模型连接测试成功。", false);
        } catch (error) { message(modelRoot, error.message, true); }
        button.disabled = false;
      });
    });

    document.querySelectorAll(".ai-delete-model").forEach(function (button) {
      button.addEventListener("click", async function () {
        if (!window.confirm("删除模型配置后，绑定它的 AI 玩家也无法运行。确认删除？")) return;
        button.disabled = true;
        try {
          await api(modelRoot, "/api/ai/models/" + encodeURIComponent(button.dataset.id) + "?expected_version=" + encodeURIComponent(button.dataset.version), "DELETE");
          window.location.reload();
        } catch (error) { button.disabled = false; message(modelRoot, error.message, true); }
      });
    });
  }

  function initProfiles() {
    if (!profileRoot) return;
    const editor = document.getElementById("ai-profile-editor");
    const form = document.getElementById("ai-profile-form");
    const title = document.getElementById("ai-profile-editor-title");
    const saveButton = document.getElementById("ai-profile-save");
    const canWrite = profileRoot.dataset.canWrite === "true";
    const provisioningAvailable = profileRoot.dataset.provisioning === "true";
    let editingSkills = [];
    let editingPersonality = {};
    let editingGoal = {};
    let initialPets = [], initialResults = [], initialSearch = 0;
    const initialSection = document.getElementById("ai-initial-section");
    const initialSummary = document.getElementById("ai-initial-summary");
    const initialPetLevelField = document.getElementById("ai-initial-pet-level-field");
    function renderInitial() {
      const mode = value(form, "initial_mode") || "birth";
      for (const part of ["custom", "random", "pets"]) {
        const node = document.getElementById("ai-initial-" + part);
        if (node) node.hidden = part === "pets" ? mode === "birth" : mode !== part;
      }
      if (initialPetLevelField) initialPetLevelField.hidden = mode !== "custom";
      const selected = document.getElementById("ai-initial-selected");
      if (!selected) return;
      selected.replaceChildren();
      initialPets.forEach(function(pet, index) {
        const row = document.createElement("div"), remove = document.createElement("button");
        row.textContent = pet.name + (mode === "custom" ? (pet.level === undefined ? " · 请移除后按指定等级重新添加" : " · 等级 " + pet.level) : "");
        remove.type = "button"; remove.textContent = "移除";
        remove.addEventListener("click", function(){initialPets.splice(index,1);renderInitial();});
        row.appendChild(remove); selected.appendChild(row);
      });
    }
    form?.elements.namedItem("initial_mode")?.addEventListener("change", renderInitial);
    document.getElementById("ai-initial-search")?.addEventListener("click", async function(){
      const revision = ++initialSearch;
      try {
        const data = await api(profileRoot, "/api/player-catalog?kind=pet&limit=100&q=" + encodeURIComponent(value(form,"initial_pet_search")), "GET");
        if (revision !== initialSearch) return;
        initialResults = Array.isArray(data.entries) ? data.entries : [];
        const select = document.getElementById("ai-initial-results");
        select.replaceChildren();
        initialResults.forEach(function(pet,index){const option=document.createElement("option");option.value=String(index);option.textContent=pet.name;select.appendChild(option);});
        if (!initialResults.length) message(profileRoot,"没有找到宠物",false);
      } catch(error) {if(revision===initialSearch){initialResults=[];document.getElementById("ai-initial-results")?.replaceChildren();message(profileRoot,error.message,true);}}
    });
    document.getElementById("ai-initial-add")?.addEventListener("click", function(){
      const index = document.getElementById("ai-initial-results")?.value;
      const pet = index === "" ? null : initialResults[Number(index)];
      if (!pet) return;
      const custom = value(form, "initial_mode") === "custom";
      const level = Number(value(form, "initial_pet_level"));
      const limit = custom ? 5 : 100;
      if (initialPets.length >= limit || (custom && (!Number.isInteger(level) || level < 1 || level > 140))) {
        message(profileRoot, "宠物数量或等级超出范围", true);
        return;
      }
      initialPets.push({template_id: pet.template_id, name: pet.name, level: custom ? level : undefined});
      renderInitial();
    });
    function initialText(profile) {
      if(profile.initial_state_unavailable) return "初始状态记录暂不可用";
      const record=profile.initial_state;
      if(!record) return "使用服务器出生状态";
      const p=record.resolved || {}, names=["萨姆吉尔村","玛丽娜丝村","加加村","卡鲁它那村"];
      const pets=(record.actual?.possessions || []).filter(p=>p.kind==="pet" && p.location==="inventory").map(p=>p.name + " " + (p.attributes?.find(a=>a.key==="lv")?.value ?? "?") + "级");
      const weights=p.weights || {}, allocation=["vital","strength","toughness","dexterity"].map(k=>weights[k] ?? 0).join(":");
      const rules=record.requested?.random;
      const bounds=rules ? " · 随机范围：人物 " + rules.character_level.min + "–" + rules.character_level.max + "级，宠物 " + rules.pet_level.min + "–" + rules.pet_level.max + "级、" + rules.pet_count.min + "–" + rules.pet_count.max + "只" : "";
      const requestedMount = Boolean(record.resolved?.mount ?? record.requested?.mount);
      const actualMount = initialRidingText(record.actual);
      const mountText = " · 默认骑宠：" + (requestedMount ? "开启" : "关闭") + "，实际 " + actualMount;
      const modeText = {birth:"出生状态",random:"随机生成",custom:"自定义"}[record.requested?.mode] || "未知";
      return "初始状态：" + modeText + " · 人物 " + p.character_level + " 级 · " + (names[p.hometown] || "出生村未知") + " · 体腕耐速权重 " + allocation + " · 宠物 " + (pets.join("、") || "无") + mountText + bounds + "。创建后保持，重启不重新生成。";
    }

    const initializationsList = document.getElementById("ai-initializations-list");
    const initializationsRefresh = document.getElementById("ai-initializations-refresh");
    const initializationsMessage = document.getElementById("ai-initializations-message");
    let initializationsRequest = 0;
    let initializationsBusy = false;
    let initializationsLoaded = false;

    function initializationsStatus(text, error) {
      if (!initializationsMessage) return;
      initializationsMessage.hidden = !text;
      initializationsMessage.textContent = text || "";
      initializationsMessage.classList.toggle("error", !!error);
      initializationsMessage.classList.toggle("success", !error);
    }

    function initializationCell(text, className) {
      const cell = document.createElement("td");
      if (className) cell.className = className;
      cell.textContent = text == null ? "" : String(text);
      return cell;
    }

    function initializationUpdatedAt(value) {
      if (!value) return "—";
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString("zh-CN");
    }

    function initializationStatusLabel(status) {
      return ({
        reserved: "已预留",
        creating: "创建中",
        applying: "初始化中",
        applied: "待发布",
        publication_pending: "待发布",
        published: "已完成",
        failed_or_unconfirmed: "待核验",
        draft: "待核验"
      })[status] || "待核验";
    }

    function renderInitializationsPlaceholder(text) {
      if (!initializationsList) return;
      initializationsList.replaceChildren();
      const empty = document.createElement("tr");
      const cell = initializationCell(text, "empty");
      cell.colSpan = 5;
      empty.appendChild(cell);
      initializationsList.appendChild(empty);
    }

    function renderInitializations(initializations) {
      if (!initializationsList) return;
      initializationsList.replaceChildren();
      if (!initializations.length) {
        renderInitializationsPlaceholder("暂无未完成的创建。");
        return;
      }
      initializations.forEach(function (item) {
        const row = document.createElement("tr");
        row.appendChild(initializationCell(item.profile_id || "—", "breakable"));
        row.appendChild(initializationCell(item.character_name || "角色身份待核验"));
        row.appendChild(initializationCell(initializationStatusLabel(item.status)));
        row.appendChild(initializationCell(initializationUpdatedAt(item.updated_at)));
        const actions = document.createElement("td");
        actions.className = "ai-actions";
        if (canWrite && item.recoverable === true && item.profile_id) {
          const recover = document.createElement("button");
          recover.type = "button";
          recover.className = "button primary ai-initialization-recover";
          recover.textContent = "恢复发布";
          recover.addEventListener("click", async function () {
            if (initializationsBusy) return;
            initializationsBusy = true;
            recover.disabled = true;
            try {
              await api(profileRoot, "/api/ai/initializations/" + encodeURIComponent(item.profile_id) + "/recover", "POST", {});
              await loadInitializations();
              initializationsStatus("恢复发布完成；AI 玩家不会自动启动，请在玩家列表查看当前状态。", false);
              if (window.location && typeof window.location.reload === "function") window.location.reload();
            } catch (error) {
              recover.disabled = false;
              initializationsStatus(error.message, true);
            } finally {
              initializationsBusy = false;
            }
          });
          actions.appendChild(recover);
        } else {
          const note = document.createElement("small");
          note.textContent = item.recoverable === true ? "仅管理员可恢复" : "待核验，不自动重试";
          actions.appendChild(note);
        }
        row.appendChild(actions);
        initializationsList.appendChild(row);
      });
    }

    async function loadInitializations() {
      if (!initializationsList) return;
      const requestID = ++initializationsRequest;
      if (initializationsRefresh) initializationsRefresh.disabled = true;
      try {
        const result = await api(profileRoot, "/api/ai/initializations", "GET");
        if (requestID !== initializationsRequest) return;
        const entries = Array.isArray(result.initializations) ? result.initializations : [];
        renderInitializations(entries);
        initializationsLoaded = true;
        initializationsStatus("", false);
      } catch (error) {
        if (requestID !== initializationsRequest) return;
        if (!initializationsLoaded) renderInitializationsPlaceholder("读取失败，请点击刷新重试。");
        initializationsStatus((error.message || "AI 创建记录读取失败") + (initializationsLoaded ? " 页面保留上次结果。" : ""), true);
      } finally {
        if (requestID === initializationsRequest && initializationsRefresh) initializationsRefresh.disabled = false;
      }
    }

    if (initializationsRefresh) initializationsRefresh.addEventListener("click", loadInitializations);
    loadInitializations();


    function selectedSkillNames() {
      if (!form) return [];
      return Array.from(form.querySelectorAll('input[name="skill_names"]:checked')).map(function (input) {
        return input.value;
      });
    }

    function syncLifePolicy() {
      const enabled = value(form, "goal_kind") === "life";
      const section = document.getElementById("ai-life-policy");
      if (section) section.hidden = !enabled;
      const interval = form?.elements.namedItem("life_decision_interval");
      if (interval) interval.disabled = !enabled;
      const stop = form?.elements.namedItem("stop_when_completed");
      if (stop) {
        stop.disabled = enabled;
        if (enabled) stop.checked = false;
      }
    }
    form?.elements.namedItem("goal_kind")?.addEventListener("change", syncLifePolicy);

    function selectSkills(profile, isNew) {
      const selected = new Set((profile.skills || []).map(function (skill) { return skill.name; }));
      form.querySelectorAll('input[name="skill_names"]').forEach(function (input) {
        input.checked = isNew ? input.value === "stoneage-play" : selected.has(input.value);
        input.disabled = isNew && input.value === "stoneage-play";
      });
    }

    function setIdentityFields(isNew) {
      const fields = [
        document.getElementById("ai-account-id-field"),
        document.getElementById("ai-account-username-field"),
        document.getElementById("ai-character-id-field")
      ];
      fields.forEach(function (field) { if (field) field.hidden = isNew; });
      const accountUsername = form.elements.namedItem("account_username");
      if (accountUsername) accountUsername.readOnly = true;
    }

    function editedSkills() {
      const selected = new Set(selectedSkillNames());
      const catalogSkills = Array.from(form.querySelectorAll('input[name="skill_names"]')).reduce(function (result, input) {
        result[input.value] = true;
        return result;
      }, {});
      const next = [];
      // Preserve old CRUD-only entries that are not part of the native
      // catalog. Native entries are rebuilt by the server from their
      // allowlisted name/version and never trust browser digests.
      editingSkills.forEach(function (skill) {
        if (!catalogSkills[skill.name]) next.push(skill);
      });
      form.querySelectorAll('input[name="skill_names"]').forEach(function (input) {
        if (!selected.has(input.value)) return;
        next.push({name: input.value, version: input.dataset.version || "", kind: "native"});
      });
      return next;
    }

    function reset() {
      if (!form) return;
      form.reset();
      editingSkills = [];
      editingPersonality = {};
      editingGoal = {};
      form.elements.namedItem("id").value = "";
      form.elements.namedItem("expected_version").value = "";
      form.elements.namedItem("status").value = "stopped";
      form.elements.namedItem("daily_token_budget").value = "100000";
      form.elements.namedItem("external_spend_limit").value = "0";
      form.elements.namedItem("unlimited_funds").checked = true;
      setIdentityFields(true);
      hide(editor);
    }

    function edit(profile) {
      if (!form || !editor) return;
      const isNew = !profile.id;
      initialSearch++;initialPets=[];initialResults=[];
      document.getElementById("ai-initial-results")?.replaceChildren();
      if(initialSection) initialSection.hidden=!isNew;
      if(initialSummary){initialSummary.hidden=isNew;initialSummary.textContent=initialText(profile);}
      const initialMode=form.elements.namedItem("initial_mode");if(initialMode) initialMode.value="birth";
      renderInitial();
      const account = profile.account || {};
      const character = profile.character || {};
      const personality = profile.personality || {};
      const goal = profile.goal || {};
      editingPersonality = personality;
      editingGoal = goal;
      editingSkills = Array.isArray(profile.skills) ? profile.skills.slice() : [];
      form.elements.namedItem("id").value = profile.id || "";
      form.elements.namedItem("expected_version").value = profile.version || "";
      form.elements.namedItem("account_id").value = account.id || "";
      form.elements.namedItem("account_username").value = account.username || "";
      form.elements.namedItem("character_id").value = character.id || "";
      form.elements.namedItem("character_name").value = character.name || "";
      form.elements.namedItem("character_name").required = isNew;
      if (isNew) form.elements.namedItem("character_slot").value = "0";
      form.elements.namedItem("model_config_id").value = profile.model_config_id || "";
      form.elements.namedItem("status").value = profile.profile_status || "stopped";
      form.elements.namedItem("daily_token_budget").value = isNew ? 100000 : (profile.daily_token_budget || 0);
      form.elements.namedItem("external_spend_limit").value = profile.external_spend_limit || 0;
      form.elements.namedItem("personality_name").value = personality.name || "";
      form.elements.namedItem("personality_prompt").value = personality.prompt || "";
      form.elements.namedItem("goal_kind").value = goal.kind || (isNew ? "life" : "");
      const lifeInterval = form.elements.namedItem("life_decision_interval");
      if (lifeInterval) lifeInterval.value = goal.life?.decision_interval_seconds || 300;
      form.elements.namedItem("target_level").value = goal.target_level || 0;
      form.elements.namedItem("goal_description").value = goal.description || "";
      form.elements.namedItem("target_character_id").value = goal.target_character_id || "";
      form.elements.namedItem("target_kind").value = (goal.metadata && (goal.metadata.target_kind || goal.metadata.target_type)) || "";
      form.elements.namedItem("stop_when_completed").checked = isNew ? true : Boolean(goal.stop_when_completed);
      syncLifePolicy();
      const build = goal.character_build;
      form.elements.namedItem("character_build_enabled").checked = Boolean(build);
      ["vital", "strength", "toughness", "dexterity"].forEach(function (attribute) {
        form.elements.namedItem("build_" + attribute).value = (build && build.weights && build.weights[attribute]) || 0;
      });
      form.elements.namedItem("build_reserve_points").value = (build && build.reserve_points) || 0;
      form.elements.namedItem("unlimited_funds").checked = profile.unlimited_funds !== false;
      selectSkills(profile, isNew);
      setIdentityFields(isNew);
      title.textContent = isNew ? "创建游戏 AI 角色" : "编辑 AI 玩家";
      if (saveButton) saveButton.textContent = isNew ? "创建游戏 AI 角色" : "保存 AI 玩家";
      show(editor);
      editor.scrollIntoView({ behavior: "smooth", block: "start" });
      form.elements.namedItem(isNew ? "character_name" : "account_id").focus();
    }

    const newButton = document.getElementById("ai-profile-new");
    if (newButton && canWrite) newButton.addEventListener("click", function () { reset(); edit({}); });
    const cancel = document.getElementById("ai-profile-cancel");
    if (cancel) cancel.addEventListener("click", reset);

    document.querySelectorAll(".ai-edit-profile, .ai-view-profile").forEach(function (button) {
      button.addEventListener("click", async function () {
        try {
          const value = await api(profileRoot, "/api/ai/profiles/" + encodeURIComponent(button.dataset.id), "GET");
          if (button.classList.contains("ai-view-profile")) {
            showAudit(button.dataset.id, value.profile || {});
          } else {
            edit(value.profile || {});
          }
        } catch (error) { message(profileRoot, error.message, true); }
      });
    });

    let auditRequest = 0;
    async function showAudit(id, profile) {
      const requestID = ++auditRequest;
      const audit = document.getElementById("ai-profile-audit");
      const content = document.getElementById("ai-profile-audit-content");
      const heading = document.getElementById("ai-profile-audit-title");
      try {
        const base = "/api/ai/profiles/" + encodeURIComponent(id);
        const results = await Promise.allSettled([
          api(profileRoot, base + "/events", "GET"),
          api(profileRoot, base + "/life-state", "GET")
        ]);
        if (requestID !== auditRequest) return;
        if (results[0].status !== "fulfilled") throw results[0].reason;
        const state = results[1].status === "fulfilled" ? results[1].value : null;
        let details = "生活记录暂不可用";
        if (state?.available) {
          details = "私人笔记（AI 自己的计划与回忆，不是已确认的游戏事实）" +
            (state.notes_truncated ? " · 仅显示最近 50 条" : "") + "\n" + JSON.stringify(state.notes || [], null, 2) +
            "\n\n定时提醒（delivered 表示已投递给模型，不代表游戏任务完成）" +
            (state.schedules_truncated ? " · 仅显示前 50 条，待处理提醒优先" : "") + "\n" + JSON.stringify(state.schedules || [], null, 2);
        }
        heading.textContent = (profile.character && profile.character.name ? profile.character.name : id) + " · 只读生活记录与审计";
        content.textContent = initialText(profile) + "\n\n" + details + "\n\n审计事件\n" + JSON.stringify(results[0].value.events || [], null, 2);
        show(audit);
        audit.scrollIntoView({ behavior: "smooth", block: "start" });
      } catch (error) { message(profileRoot, error.message, true); }
    }
    const closeAudit = document.getElementById("ai-profile-audit-close");
    if (closeAudit) closeAudit.addEventListener("click", function () { auditRequest++; hide(document.getElementById("ai-profile-audit")); });

    document.querySelectorAll(".ai-profile-control").forEach(function (button) {
      button.addEventListener("click", async function () {
        button.disabled = true;
        try {
          await api(profileRoot, "/api/ai/profiles/" + encodeURIComponent(button.dataset.id) + "/" + button.dataset.action, "POST", {});
          window.location.reload();
        } catch (error) {
          button.disabled = false;
          message(profileRoot, error.message, true);
        }
      });
    });

    let recovery = null;
    let recoveryBusy = false;
    let recoveryLoad = 0;
    const recoveryPanel = document.getElementById("ai-recovery-panel");
    const recoveryAck = document.getElementById("ai-recovery-ack");
    const recoverySubmit = document.getElementById("ai-recovery-submit");
    const recoveryMessage = document.getElementById("ai-recovery-message");
    document.querySelectorAll(".ai-profile-recovery").forEach(function (button) {
      button.addEventListener("click", async function () {
        if (recoveryBusy) return;
        const load = ++recoveryLoad;
        button.disabled = true;
        try {
          const result = await api(profileRoot, "/api/ai/profiles/" + encodeURIComponent(button.dataset.id) + "/recovery", "GET");
          if (load !== recoveryLoad) return;
          recovery = result.recovery;
          if (!recovery) { hide(recoveryPanel); message(profileRoot, "没有待核查的未知轮次。", false); return; }
          recoveryAck.checked = false; recoverySubmit.disabled = true; recoveryMessage.textContent = "";
          document.getElementById("ai-recovery-summary").textContent = "轮次 " + recovery.attempt_id + " · 保留预算 " + recovery.reserved_tokens + " tokens。" + (recovery.ready ? "可以核查。" : recovery.execution && recovery.execution.container_stopped === false ? "遗留模型容器尚未退出，请稍后重新打开核查。" : "请先停止 AI 玩家，然后重新打开核查。");
          show(recoveryPanel); recoveryPanel.scrollIntoView({ behavior: "smooth", block: "start" });
        } catch (error) { message(profileRoot, error.message, true); }
        finally { button.disabled = false; }
      });
    });
    if (recoveryAck) recoveryAck.addEventListener("change", function () { recoverySubmit.disabled = recoveryBusy || !recovery || !recovery.ready || !recoveryAck.checked; });
    const recoveryClose = document.getElementById("ai-recovery-close");
    if (recoveryClose) recoveryClose.addEventListener("click", function () { recoveryLoad++; recovery = null; hide(recoveryPanel); });
    if (recoverySubmit && canWrite) recoverySubmit.addEventListener("click", async function () {
      if (recoveryBusy || !recovery || !recovery.ready || !recoveryAck.checked) return;
      const reviewing = recovery;
      recoveryBusy = true; recoveryAck.disabled = true; recoverySubmit.disabled = true;
      try {
        await api(profileRoot, "/api/ai/profiles/" + encodeURIComponent(reviewing.profile_id) + "/recovery", "POST", Object.assign({}, reviewing, { reason: "accept_uncertain_outcome" }));
        if (recovery !== reviewing) return;
        recovery = null; recoveryAck.checked = false;
        recoveryMessage.textContent = "核查完成，未知结果和预算已保留。可手动启动新轮次。";
      } catch (error) {
        if (recovery === reviewing) {
          recoveryMessage.textContent = error.message;
          recoverySubmit.disabled = false;
        }
      } finally { recoveryBusy = false; recoveryAck.disabled = false; }
    });

    if (form && canWrite) form.addEventListener("submit", async function (event) {
      event.preventDefault();
      const id = value(form, "id");
      const goal = Object.assign({}, editingGoal, {
        kind: value(form, "goal_kind"), description: value(form, "goal_description"),
        target_level: integer(form, "target_level"), target_character_id: value(form, "target_character_id"),
        stop_when_completed: checked(form, "stop_when_completed")
      });
      const metadata = Object.assign({}, editingGoal.metadata || {});
      const targetKind = value(form, "target_kind");
      if (targetKind) metadata.target_kind = targetKind;
      else { delete metadata.target_kind; delete metadata.target_type; }
      goal.metadata = metadata;
      if (goal.kind === "life") {
        goal.life = Object.assign({}, editingGoal.life || {}, { decision_interval_seconds: integer(form, "life_decision_interval") || 300 });
        goal.stop_when_completed = false;
      } else { delete goal.life; }
      if (checked(form, "character_build_enabled")) {
        goal.character_build = {
          weights: { vital: integer(form, "build_vital"), strength: integer(form, "build_strength"), toughness: integer(form, "build_toughness"), dexterity: integer(form, "build_dexterity") },
          reserve_points: integer(form, "build_reserve_points")
        };
      } else { delete goal.character_build; }
      const profileFields = {
        model_config_id: value(form, "model_config_id"),
        personality: Object.assign({}, editingPersonality, { name: value(form, "personality_name"), prompt: value(form, "personality_prompt") }),
        goal,
        unlimited_funds: checked(form, "unlimited_funds"),
        daily_token_budget: integer(form, "daily_token_budget"), external_spend_limit: integer(form, "external_spend_limit"), status: value(form, "status")
      };
      const body = id ? Object.assign({}, profileFields, {
        skills: editedSkills(),
        account: { id: value(form, "account_id"), username: value(form, "account_username") },
        character: { id: value(form, "character_id"), name: value(form, "character_name") }
      }) : Object.assign({}, profileFields, {
        character_name: value(form, "character_name"), character_slot: integer(form, "character_slot"),
        skill_names: selectedSkillNames()
      });
      if (id) body.expected_version = integer(form, "expected_version");
      try {
        if (!id && !provisioningAvailable) throw new Error("AI 游戏角色创建服务尚未配置");
        if (!id) body.initial_state=initialConfig(form,initialPets);
        await api(profileRoot, id ? "/api/ai/profiles/" + encodeURIComponent(id) : "/api/ai/profiles/provision", id ? "PATCH" : "POST", body);
        window.location.reload();
      } catch (error) { message(profileRoot, error.message, true); }
    });

    document.querySelectorAll(".ai-delete-profile").forEach(function (button) {
      button.addEventListener("click", async function () {
        if (!window.confirm("删除 AI 玩家配置及其运行检查点，确认继续？")) return;
        button.disabled = true;
        try {
          await api(profileRoot, "/api/ai/profiles/" + encodeURIComponent(button.dataset.id) + "?expected_version=" + encodeURIComponent(button.dataset.version), "DELETE");
          window.location.reload();
        } catch (error) { button.disabled = false; message(profileRoot, error.message, true); }
      });
    });
  }

  initModels();
  initProfiles();
})();
