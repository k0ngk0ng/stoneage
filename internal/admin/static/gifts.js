"use strict";

(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) {
    module.exports = api;
    return;
  }
  root.StoneAgeGifts = api;
  if (root.document) {
    const start = function () { api.init(root.document, root); };
    if (root.document.readyState === "loading") {
      root.document.addEventListener("DOMContentLoaded", start, {once: true});
    } else {
      start();
    }
  }
}(typeof globalThis === "object" ? globalThis : this, function () {
  const DEFAULT_LIMIT = 40;
  const KIND_LABELS = {item: "物品", pet: "宠物"};
  const RUN_STATUS_LABELS = {
    pending: "等待处理", running: "处理中", completed: "已完成", failed: "失败", uncertain: "待核实"
  };
  const DELIVERY_STATUS_LABELS = {
    pending: "等待处理", sending: "发送中", applied: "已发放", skip_capacity: "容量不足，整角色跳过",
    failed: "失败", uncertain: "待核实"
  };

  function asArray(value) {
    if (Array.isArray(value)) return value;
    if (!value || typeof value === "string") return [];
    try { return Array.from(value); } catch (_) { return []; }
  }

  function text(value, fallback) {
    if (value === undefined || value === null || value === "") {
      return fallback === undefined ? "" : String(fallback);
    }
    return String(value);
  }

  function safeID(value) {
    const raw = text(value).trim();
    if (!raw) return "";
    const number = Number(raw);
    return Number.isSafeInteger(number) ? number : raw;
  }

  function positiveInteger(value, fallback) {
    const number = Number(String(value === undefined || value === null ? "" : value).trim());
    return Number.isSafeInteger(number) && number > 0 ? number : fallback === undefined ? null : fallback;
  }

  function slotNumber(value) {
    const number = Number(String(value === undefined || value === null ? "" : value).trim());
    return Number.isSafeInteger(number) && number >= 0 && number <= 1 ? number : null;
  }

  function idKey(value) {
    return text(value).trim();
  }

  function catalogName(entry, kind) {
    return text(entry && (entry.name || entry.label || entry.title), KIND_LABELS[kind] || "目录项") || (KIND_LABELS[kind] || "目录项");
  }

  function normalizeCatalogEntry(entry, kind) {
    if (!entry) return null;
    const sourceID = entry.id === undefined || entry.id === null || entry.id === ""
      ? entry.template_id
      : entry.id;
    if (sourceID === undefined || sourceID === null || sourceID === "") return null;
    const id = safeID(sourceID);
    if (id === "") return null;
    return Object.assign({}, entry, {id: id, kind: kind, name: catalogName(entry, kind)});
  }

  function catalogIDs(entry) {
    if (!entry) return [];
    return [entry.template_id, entry.id].filter(function (value) {
      return value !== undefined && value !== null && value !== "";
    }).map(idKey);
  }

  function catalogEntryMatches(entry, id) {
    const wanted = idKey(id);
    return Boolean(wanted && catalogIDs(entry).some(function (value) { return value === wanted; }));
  }

  function catalogEntriesMatch(left, right) {
    const rightIDs = catalogIDs(right);
    return catalogIDs(left).some(function (value) { return rightIDs.includes(value); });
  }

  function catalogEntryName(entry, kind) {
    const value = text(entry && (entry.name || entry.label || entry.title)).trim();
    return value && value !== KIND_LABELS[kind] ? value : "";
  }

  function normalizeCatalogPayload(payload, kind) {
    const source = payload && (payload.entries || payload.results || payload.items || payload.pets);
    const entries = asArray(source).map(function (entry) { return normalizeCatalogEntry(entry, kind); }).filter(Boolean);
    return {entries: entries, total: Number.isFinite(Number(payload && payload.total)) ? Number(payload.total) : entries.length};
  }

  function normalizeGiftEntry(entry, kind) {
    if (!entry) return null;
    const sourceID = entry.template_id === undefined || entry.template_id === null || entry.template_id === ""
      ? entry.id
      : entry.template_id;
    if (sourceID === undefined || sourceID === null || sourceID === "") return null;
    const id = safeID(sourceID);
    const quantity = positiveInteger(entry.quantity, 1);
    if (id === "" || quantity === null) return null;
    return {
      id: id,
      quantity: quantity,
      name: text(entry.name || entry.label || entry.title, KIND_LABELS[kind] + " #" + text(id)),
      kind: kind,
      source: entry
    };
  }

  function normalizeDefinition(definition) {
    const value = definition && typeof definition === "object" ? definition : {};
    return {
      items: asArray(value.items).map(function (entry) { return normalizeGiftEntry(entry, "item"); }).filter(Boolean),
      pets: asArray(value.pets).map(function (entry) { return normalizeGiftEntry(entry, "pet"); }).filter(Boolean)
    };
  }

  function normalizeGiftPackage(packageValue) {
    if (!packageValue || packageValue.id === undefined || packageValue.id === null || packageValue.id === "") return null;
    const id = safeID(packageValue.id);
    if (id === "") return null;
    return {
      id: id,
      name: text(packageValue.name, "未命名礼包"),
      definition: normalizeDefinition(packageValue.definition),
      created_at: packageValue.created_at,
      updated_at: packageValue.updated_at,
      source: packageValue
    };
  }

  function packageListFromResponse(payload) {
    const source = payload && (payload.packages || payload.gift_packages || payload.results);
    return asArray(source).map(normalizeGiftPackage).filter(Boolean);
  }

  function mergeCatalogEntries(catalog, kind, entries) {
    const current = catalog && catalog[kind];
    if (!current) return;
    asArray(entries).forEach(function (entry) {
      const index = current.findIndex(function (candidate) { return catalogEntriesMatch(candidate, entry); });
      if (index >= 0) current[index] = entry; else current.push(entry);
    });
  }

  function definitionPayload(items, pets) {
    function compact(entries) {
      return asArray(entries).map(function (entry) {
        const sourceID = entry && entry.template_id !== undefined && entry.template_id !== null && entry.template_id !== ""
          ? entry.template_id
          : entry && entry.id;
        const id = safeID(sourceID);
        const quantity = positiveInteger(entry && entry.quantity, null);
        return id !== "" && quantity !== null ? {id: id, quantity: quantity} : null;
      }).filter(Boolean);
    }
    return {items: compact(items), pets: compact(pets)};
  }

  function buildGiftRunPayload(values) {
    const source = values || {};
    const scope = source.scope === "all" ? "all" : "single";
    const payload = {package_id: safeID(source.package_id), scope: scope};
    if (scope === "single") {
      const accountID = positiveInteger(source.account_id, null);
      const slot = slotNumber(source.character_slot);
      payload.account_id = accountID;
      payload.character_slot = slot;
      const username = text(source.account_username).trim();
      if (username) payload.account_username = username;
    }
    const previewToken = text(source.preview_token).trim();
    if (previewToken) payload.preview_token = previewToken;
    return payload;
  }

  function previewCount(value) {
    if (Array.isArray(value)) return value.length;
    const number = Number(value);
    return Number.isFinite(number) && number >= 0 ? Math.trunc(number) : 0;
  }

  function previewSummary(payload) {
    const value = payload || {};
    const skipped = asArray(value.skipped);
    return {
      targets: previewCount(value.targets ?? value.total ?? value.target_count),
      eligible: previewCount(value.eligible !== undefined ? value.eligible : value.eligible_count),
      skipped: skipped,
      previewToken: text(value.preview_token || value.previewToken).trim()
    };
  }

  function clear(element) {
    if (!element) return;
    while (element.firstChild) element.removeChild(element.firstChild);
    if (Array.isArray(element.children)) {
      while (element.children.length) element.removeChild(element.children[0]);
    }
  }

  function makeElement(document, tag, className, content) {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (content !== undefined && content !== null) element.textContent = String(content);
    return element;
  }

  function append(parent) {
    for (let index = 1; index < arguments.length; index++) {
      const child = arguments[index];
      if (!child || !parent) continue;
      if (typeof parent.appendChild === "function") parent.appendChild(child);
      else if (typeof parent.append === "function") parent.append(child);
    }
    return parent;
  }

  function setText(element, value) {
    if (element) element.textContent = value === undefined || value === null ? "" : String(value);
  }

  function setAttribute(element, name, value) {
    if (element && typeof element.setAttribute === "function") element.setAttribute(name, String(value));
    else if (element) {
      if (!element.attributes) element.attributes = {};
      element.attributes[name] = String(value);
    }
  }

  function setClass(element, name, enabled) {
    if (!element || !element.classList) return;
    if (typeof element.classList.toggle === "function") element.classList.toggle(name, Boolean(enabled));
    else if (enabled && typeof element.classList.add === "function") element.classList.add(name);
    else if (!enabled && typeof element.classList.remove === "function") element.classList.remove(name);
  }

  function textInputValue(input) {
    return text(input && input.value).trim();
  }

  function requestURL(kind, query) {
    const params = new URLSearchParams();
    params.set("kind", kind);
    params.set("q", query === undefined || query === null ? "" : String(query));
    params.set("offset", "0");
    params.set("limit", String(DEFAULT_LIMIT));
    return "/api/player-catalog?" + params.toString();
  }

  function pageURL(path, cursor) {
    const value = String(path || "");
    if (!cursor) return value;
    return value + (value.indexOf("?") >= 0 ? "&" : "?") + "cursor=" + encodeURIComponent(String(cursor)) + "&limit=100";
  }

  function errorMessage(error, fallback) {
    return text(error && error.message, fallback || "请求失败，请稍后重试。");
  }

  function init(document, windowObject, fetchImplementation) {
    const root = document && document.getElementById ? document.getElementById("gift-admin") : null;
    if (!root) return null;
    const fetcher = fetchImplementation || (windowObject && windowObject.fetch ? windowObject.fetch.bind(windowObject) : null);
    if (!fetcher) return null;

    const refs = {
      root: root,
      status: document.getElementById("gift-status"),
      refresh: document.getElementById("gift-refresh"),
      packages: document.getElementById("gift-package-list"),
      newPackage: document.getElementById("gift-new-package"),
      editorTitle: document.getElementById("gift-editor-title"),
      editorMode: document.getElementById("gift-editor-mode"),
      packageForm: document.getElementById("gift-package-form"),
      packageID: document.getElementById("gift-package-id"),
      packageName: document.getElementById("gift-package-name"),
      itemSearch: document.getElementById("gift-item-search"),
      itemQuery: document.getElementById("gift-item-query"),
      itemResults: document.getElementById("gift-item-results"),
      itemSelected: document.getElementById("gift-item-selected"),
      petSearch: document.getElementById("gift-pet-search"),
      petQuery: document.getElementById("gift-pet-query"),
      petResults: document.getElementById("gift-pet-results"),
      petSelected: document.getElementById("gift-pet-selected"),
      definitionHint: document.getElementById("gift-definition-hint"),
      packageSave: document.getElementById("gift-package-save"),
      runForm: document.getElementById("gift-run-form"),
      runPackage: document.getElementById("gift-run-package"),
      singleTarget: document.getElementById("gift-single-target"),
      accountID: document.getElementById("gift-account-id"),
      accountUsername: document.getElementById("gift-account-username"),
      characterSlot: document.getElementById("gift-character-slot"),
      loadCharacters: document.getElementById("gift-load-characters"),
      characterHint: document.getElementById("gift-character-hint"),
      previewPanel: document.getElementById("gift-preview-panel"),
      previewStats: document.getElementById("gift-preview-stats"),
      previewWarning: document.getElementById("gift-preview-warning"),
      previewSkipped: document.getElementById("gift-preview-skipped"),
      previewReset: document.getElementById("gift-preview-reset"),
      confirm: document.getElementById("gift-confirm"),
      runResult: document.getElementById("gift-run-result"),
      runsRefresh: document.getElementById("gift-runs-refresh"),
      runs: document.getElementById("gift-runs-list"),
      runsMore: document.getElementById("gift-runs-more"),
      runDetail: document.getElementById("gift-run-detail"),
      runDetailTitle: document.getElementById("gift-run-detail-title"),
      runDetailClose: document.getElementById("gift-run-detail-close"),
      deliveries: document.getElementById("gift-run-deliveries"),
      deliveriesMore: document.getElementById("gift-deliveries-more")
    };

    const state = {
      csrf: text(root.dataset && root.dataset.csrf),
      packages: [],
      editingPackageID: null,
      editorRequest: 0,
      selected: {item: [], pet: []},
      catalog: {item: [], pet: []},
      catalogRequest: {item: 0, pet: 0},
      packageRequest: 0,
      runRequest: 0,
      characterRequest: 0,
      runs: [],
      runsNextCursor: "",
      runsLoading: false,
      scope: "single",
      preview: null,
      busy: false,
      characters: [],
      detailRunID: null,
      deliveries: [],
      deliveriesNextCursor: "",
      deliveriesLoading: false
    };

    function setStatus(message, kind) {
      setText(refs.status, message || "");
      setClass(refs.status, "error", kind === "error");
      setClass(refs.status, "success", kind === "success");
    }

    async function request(path, options) {
      const settings = Object.assign({}, options || {});
      const method = String(settings.method || "GET").toUpperCase();
      const headers = Object.assign({Accept: "application/json"}, settings.headers || {});
      let body = settings.body;
      if (body !== undefined && body !== null && typeof body !== "string") {
        body = JSON.stringify(body);
        headers["Content-Type"] = "application/json";
      }
      if (method !== "GET" && method !== "HEAD" && state.csrf) headers["X-CSRF-Token"] = state.csrf;
      settings.method = method;
      settings.headers = headers;
      if (body !== undefined) settings.body = body;
      const response = await fetcher(path, settings);
      let payload = null;
      try { payload = await response.json(); } catch (_) { payload = null; }
      if (!response.ok) {
        const error = new Error(text(payload && payload.error, "请求失败（" + text(response.status, "未知状态") + "）"));
        error.status = response.status;
        error.payload = payload;
        throw error;
      }
      return payload || {};
    }

    function setBusy(busy) {
      state.busy = Boolean(busy);
      setClass(refs.root, "busy", state.busy);
      const controls = [refs.packageSave, refs.newPackage, refs.refresh, refs.confirm, refs.previewReset, refs.runsRefresh, refs.runsMore, refs.deliveriesMore, refs.loadCharacters];
      controls.forEach(function (control) { if (control) control.disabled = state.busy; });
      if (refs.confirm) {
        const summary = state.preview && previewSummary(state.preview.result);
        refs.confirm.disabled = state.busy || !summary || !summary.previewToken || summary.eligible <= 0;
      }
    }

    function packageDefinitionSummary(packageValue) {
      const definition = normalizeDefinition(packageValue && packageValue.definition);
      const itemCount = definition.items.reduce(function (total, entry) { return total + entry.quantity; }, 0);
      const petCount = definition.pets.reduce(function (total, entry) { return total + entry.quantity; }, 0);
      return itemCount + " 件物品 · " + petCount + " 只宠物";
    }

    function renderPackageOptions() {
      if (!refs.runPackage) return;
      const current = text(refs.runPackage.value);
      clear(refs.runPackage);
      const placeholder = makeElement(document, "option", "", "请选择礼包");
      placeholder.value = "";
      refs.runPackage.appendChild(placeholder);
      state.packages.forEach(function (packageValue) {
        const option = makeElement(document, "option");
        option.value = text(packageValue.id);
        option.textContent = text(packageValue.name, "未命名礼包") + "（" + packageDefinitionSummary(packageValue) + "）";
        refs.runPackage.appendChild(option);
      });
      if (state.packages.some(function (packageValue) { return text(packageValue.id) === current; })) refs.runPackage.value = current;
    }

    function renderPackages() {
      if (!refs.packages) return;
      clear(refs.packages);
      if (!state.packages.length) {
        append(refs.packages, makeElement(document, "div", "empty", "暂无礼包，先新建一个礼包。"));
        renderPackageOptions();
        return;
      }
      state.packages.forEach(function (packageValue) {
        const row = makeElement(document, "article", "gift-package-row");
        setClass(row, "selected", text(state.editingPackageID) === text(packageValue.id));
        const copy = makeElement(document, "div", "gift-package-copy");
        append(copy,
          makeElement(document, "strong", "", packageValue.name),
          makeElement(document, "small", "", "ID " + text(packageValue.id) + " · " + packageDefinitionSummary(packageValue)));
        const actions = makeElement(document, "div", "gift-package-actions");
        const edit = makeElement(document, "button", "button", "编辑");
        edit.type = "button";
        edit.addEventListener("click", function () { editPackage(packageValue); });
        const remove = makeElement(document, "button", "danger", "删除");
        remove.type = "button";
        remove.addEventListener("click", function () { deletePackage(packageValue); });
        append(actions, edit, remove); append(row, copy, actions); refs.packages.appendChild(row);
      });
      renderPackageOptions();
    }

    function findSelected(kind, id) {
      return state.selected[kind].find(function (entry) { return idKey(entry.id) === idKey(id); }) || null;
    }

    function renderSelected(kind) {
      const target = kind === "item" ? refs.itemSelected : refs.petSelected;
      if (!target) return;
      clear(target);
      const entries = state.selected[kind];
      const heading = makeElement(document, "div", "gift-selected-title", "已选" + KIND_LABELS[kind] + "（" + entries.length + "）");
      target.appendChild(heading);
      if (!entries.length) {
        target.appendChild(makeElement(document, "div", "empty", "尚未添加" + KIND_LABELS[kind] + "。"));
        return;
      }
      entries.forEach(function (entry) {
        const row = makeElement(document, "div", "gift-selected-row");
        const copy = makeElement(document, "div", "gift-selected-copy");
        append(copy,
          makeElement(document, "strong", "", entry.name || (KIND_LABELS[kind] + " #" + text(entry.id))),
          makeElement(document, "small", "", "ID " + text(entry.id)));
        const quantity = document.createElement("input");
        quantity.type = "number"; quantity.min = "1"; quantity.step = "1"; quantity.value = String(entry.quantity);
        const maximum = kind === "item" ? 15 : 5;
        quantity.max = String(maximum);
        setAttribute(quantity, "aria-label", entry.name + "数量");
        quantity.className = "gift-selected-quantity";
        quantity.setAttribute("inputmode", "numeric");
        quantity.addEventListener("change", function () {
          const next = positiveInteger(quantity.value, null);
          if (next === null || next > maximum) {
            quantity.value = String(entry.quantity);
            setStatus(KIND_LABELS[kind] + "数量须为 1 到 " + maximum + " 的整数。", "error");
            return;
          }
          entry.quantity = next;
          quantity.value = String(entry.quantity);
          invalidatePreview();
        });
        const actions = makeElement(document, "div", "gift-selected-actions");
        const down = makeElement(document, "button", "", "−"); down.type = "button";
        const up = makeElement(document, "button", "", "+"); up.type = "button";
        const remove = makeElement(document, "button", "", "×"); remove.type = "button";
        setAttribute(down, "aria-label", "减少数量"); setAttribute(up, "aria-label", "增加数量"); setAttribute(remove, "aria-label", "移除");
        down.addEventListener("click", function () { entry.quantity = Math.max(1, entry.quantity - 1); quantity.value = String(entry.quantity); invalidatePreview(); });
        up.addEventListener("click", function () { entry.quantity = Math.min(maximum, entry.quantity + 1); quantity.value = String(entry.quantity); invalidatePreview(); });
        remove.addEventListener("click", function () {
          state.selected[kind] = state.selected[kind].filter(function (candidate) { return candidate !== entry; });
          renderSelected(kind); invalidatePreview();
        });
        append(actions, down, up, remove); append(row, copy, quantity, actions); target.appendChild(row);
      });
    }

    function editorEntryCurrent(kind, entry, packageID, requestID) {
      return state.editorRequest === requestID && idKey(state.editingPackageID) === idKey(packageID) &&
        state.selected[kind].some(function (candidate) { return candidate === entry; });
    }

    function applyCatalogName(kind, entry, packageID, requestID, catalogEntry) {
      if (!editorEntryCurrent(kind, entry, packageID, requestID)) return false;
      const name = catalogEntryName(catalogEntry, kind);
      if (!name) return false;
      entry.name = name;
      renderSelected(kind);
      return true;
    }

    async function lookupCatalogEntry(kind, id) {
      const cached = state.catalog[kind].find(function (entry) { return catalogEntryMatches(entry, id); });
      if (cached && catalogEntryName(cached, kind)) return cached;
      const payload = await request(requestURL(kind, id));
      const result = normalizeCatalogPayload(payload, kind);
      mergeCatalogEntries(state.catalog, kind, result.entries);
      return result.entries.find(function (entry) { return catalogEntryMatches(entry, id); }) || null;
    }

    function hydrateSelectedNames(packageID, requestID) {
      ["item", "pet"].forEach(function (kind) {
        state.selected[kind].slice().forEach(function (entry) {
          const fallback = KIND_LABELS[kind] + " #" + text(entry.id);
          if (entry.name && entry.name !== fallback) return;
          const cached = state.catalog[kind].find(function (catalogEntry) { return catalogEntryMatches(catalogEntry, entry.id); });
          if (applyCatalogName(kind, entry, packageID, requestID, cached)) return;
          lookupCatalogEntry(kind, entry.id).then(function (catalogEntry) {
            applyCatalogName(kind, entry, packageID, requestID, catalogEntry);
          }).catch(function () {
            // Keep the ID fallback visible when the catalog is unavailable.
          });
        });
      });
    }

    function renderCatalogResults(kind) {
      const target = kind === "item" ? refs.itemResults : refs.petResults;
      if (!target) return;
      clear(target);
      const entries = state.catalog[kind];
      if (!entries.length) {
        target.appendChild(makeElement(document, "div", "empty", "没有找到" + KIND_LABELS[kind] + "。"));
        return;
      }
      entries.forEach(function (entry) {
        const row = makeElement(document, "div", "gift-catalog-entry");
        const copy = makeElement(document, "div", "gift-catalog-entry-copy");
        const templateID = entry.template_id === undefined || entry.template_id === null || entry.template_id === ""
          ? entry.id
          : entry.template_id;
        append(copy,
          makeElement(document, "strong", "", entry.name),
          makeElement(document, "small", "", "ID " + text(templateID) + (entry.description ? " · " + text(entry.description) : "")));
        const addButton = makeElement(document, "button", "button", findSelected(kind, templateID) ? "已添加" : "添加");
        addButton.type = "button"; addButton.disabled = Boolean(findSelected(kind, templateID));
        addButton.addEventListener("click", function () {
          if (!findSelected(kind, templateID)) state.selected[kind].push({id: safeID(templateID), name: entry.name, quantity: 1, source: entry});
          renderCatalogResults(kind); renderSelected(kind); invalidatePreview();
        });
        append(row, copy, addButton); target.appendChild(row);
      });
    }

    async function searchCatalog(kind, query) {
      const requestID = ++state.catalogRequest[kind];
      const target = kind === "item" ? refs.itemResults : refs.petResults;
      if (target) { clear(target); target.appendChild(makeElement(document, "div", "empty", "正在搜索" + KIND_LABELS[kind] + "…")); }
      try {
        const payload = await request(requestURL(kind, query));
        if (requestID !== state.catalogRequest[kind]) return payload;
        const result = normalizeCatalogPayload(payload, kind);
        state.catalog[kind] = result.entries; renderCatalogResults(kind);
        return result;
      } catch (error) {
        if (requestID === state.catalogRequest[kind] && target) {
          clear(target); target.appendChild(makeElement(document, "div", "empty", errorMessage(error, "目录搜索失败。")));
        }
        throw error;
      }
    }

    function resetEditor() {
      state.editorRequest += 1;
      state.editingPackageID = null;
      state.selected = {item: [], pet: []};
      if (refs.packageID) refs.packageID.value = "";
      if (refs.packageName) refs.packageName.value = "";
      setText(refs.editorTitle, "新建礼包"); setText(refs.editorMode, "未保存");
      setText(refs.definitionHint, "礼包至少需要一个物品或宠物。");
      renderSelected("item"); renderSelected("pet"); renderPackages();
    }

    function editPackage(packageValue) {
      const normalized = normalizeGiftPackage(packageValue); if (!normalized) return;
      const editorRequest = ++state.editorRequest;
      state.editingPackageID = normalized.id;
      state.selected = {
        item: normalized.definition.items.map(function (entry) { return Object.assign({}, entry); }),
        pet: normalized.definition.pets.map(function (entry) { return Object.assign({}, entry); })
      };
      if (refs.packageID) refs.packageID.value = text(normalized.id);
      if (refs.packageName) refs.packageName.value = normalized.name;
      setText(refs.editorTitle, "编辑礼包"); setText(refs.editorMode, "ID " + text(normalized.id));
      setText(refs.definitionHint, "修改内容后保存，发放预览会使用最新定义。");
      renderSelected("item"); renderSelected("pet"); renderPackages();
      hydrateSelectedNames(normalized.id, editorRequest);
      if (refs.packageName && typeof refs.packageName.focus === "function") refs.packageName.focus();
    }

    function currentDefinition() {
      return definitionPayload(state.selected.item, state.selected.pet);
    }

    async function savePackage() {
      const name = textInputValue(refs.packageName);
      const definition = currentDefinition();
      if (!name) { setStatus("请填写礼包名称。", "error"); if (refs.packageName && refs.packageName.focus) refs.packageName.focus(); return false; }
      if (!definition.items.length && !definition.pets.length) { setStatus("礼包至少需要一个物品或宠物。", "error"); return false; }
      const editingID = state.editingPackageID;
      setBusy(true); setStatus(editingID === null ? "正在创建礼包…" : "正在保存礼包…");
      try {
        const path = editingID === null ? "/api/gift-packages" : "/api/gift-packages/" + encodeURIComponent(text(editingID));
        const payload = await request(path, {method: editingID === null ? "POST" : "PUT", body: {name: name, definition: definition}});
        const returned = normalizeGiftPackage(payload && (payload.package || payload.gift_package || payload));
        if (returned) {
          const index = state.packages.findIndex(function (packageValue) { return idKey(packageValue.id) === idKey(returned.id); });
          if (index >= 0) state.packages[index] = returned; else state.packages.push(returned);
        } else {
          await loadPackages();
        }
        setStatus("礼包已保存。", "success"); resetEditor(); return true;
      } catch (error) { setStatus(errorMessage(error, "礼包保存失败。"), "error"); return false; }
      finally { setBusy(false); }
    }

    function confirmDelete(message) {
      const confirmFunction = windowObject && typeof windowObject.confirm === "function" ? windowObject.confirm.bind(windowObject) : null;
      return confirmFunction ? confirmFunction(message) : true;
    }

    async function deletePackage(packageValue) {
      if (!packageValue || !confirmDelete("确定删除礼包“" + text(packageValue.name, "未命名礼包") + "”吗？")) return false;
      setBusy(true); setStatus("正在删除礼包…");
      try {
        await request("/api/gift-packages/" + encodeURIComponent(text(packageValue.id)), {method: "DELETE"});
        state.packages = state.packages.filter(function (candidate) { return idKey(candidate.id) !== idKey(packageValue.id); });
        if (idKey(state.editingPackageID) === idKey(packageValue.id)) resetEditor(); else renderPackages();
        setStatus("礼包已删除。", "success"); return true;
      } catch (error) { setStatus(errorMessage(error, "礼包删除失败。"), "error"); return false; }
      finally { setBusy(false); }
    }

    function setScope(scope) {
      state.scope = scope === "all" ? "all" : "single";
      if (refs.singleTarget) refs.singleTarget.hidden = state.scope !== "single";
      invalidatePreview();
    }

    function renderCharacterOptions(characters) {
      if (!refs.characterSlot) return;
      const previous = text(refs.characterSlot.value);
      clear(refs.characterSlot);
      const source = asArray(characters);
      if (!source.length) {
        [0, 1].forEach(function (slot) {
          const option = makeElement(document, "option", "", "#" + slot);
          option.value = String(slot); refs.characterSlot.appendChild(option);
        });
        if (["0", "1"].includes(previous)) refs.characterSlot.value = previous;
        refs.characterSlot.disabled = false;
        return;
      }
      source.forEach(function (character) {
        const slot = slotNumber(character && (character.slot !== undefined ? character.slot : character.character_slot));
        if (slot === null) return;
        const name = text(character.name || character.character_name, "未命名角色");
        const option = makeElement(document, "option", "", "#" + slot + " · " + name + (character.online === true ? "（在线）" : ""));
        option.value = String(slot);
        const accountID = character.account_id || character.accountID;
        const username = character.account_username || character.username;
        if (accountID !== undefined) setAttribute(option, "data-account-id", accountID);
        if (username !== undefined) setAttribute(option, "data-account-username", username);
        refs.characterSlot.appendChild(option);
      });
      if (refs.characterSlot.options && refs.characterSlot.options.length && asArray(refs.characterSlot.options).some(function (option) { return option.value === previous; })) refs.characterSlot.value = previous;
      refs.characterSlot.disabled = !(refs.characterSlot.options && refs.characterSlot.options.length);
    }

    async function loadCharacters() {
      const accountID = positiveInteger(textInputValue(refs.accountID), null);
      const username = textInputValue(refs.accountUsername);
      if (accountID === null && !username) { setStatus("读取角色需要账号 ID 或账号名。", "error"); return false; }
      const requestID = ++state.characterRequest;
      setText(refs.characterHint, "正在读取角色…");
      if (refs.loadCharacters) refs.loadCharacters.disabled = true;
      try {
        const params = new URLSearchParams();
        if (accountID !== null) params.set("account_id", String(accountID));
        else params.set("account_username", username);
        const payload = await request("/api/gift-characters?" + params.toString());
        if (requestID !== state.characterRequest) return false;
        state.characters = Array.isArray(payload) ? payload : asArray(payload && (payload.characters || payload.players));
        const resolvedAccountID = payload && (payload.account_id || payload.accountID) || state.characters.find(function (character) { return character && (character.account_id || character.accountID); });
        const resolvedUsername = payload && (payload.account_username || payload.username) || state.characters.find(function (character) { return character && (character.account_username || character.username); });
        const idValue = resolvedAccountID && typeof resolvedAccountID === "object" ? (resolvedAccountID.account_id || resolvedAccountID.accountID) : resolvedAccountID;
        const nameValue = resolvedUsername && typeof resolvedUsername === "object" ? (resolvedUsername.account_username || resolvedUsername.username) : resolvedUsername;
        if (idValue !== undefined && idValue !== null && refs.accountID && !textInputValue(refs.accountID)) refs.accountID.value = String(idValue);
        if (nameValue !== undefined && nameValue !== null && refs.accountUsername && !textInputValue(refs.accountUsername)) refs.accountUsername.value = String(nameValue);
        renderCharacterOptions(state.characters);
        setText(refs.characterHint, state.characters.length ? "已读取 " + state.characters.length + " 个角色。" : "未返回角色列表，可直接选择角色槽位提交。");
        return true;
      } catch (error) { setText(refs.characterHint, errorMessage(error, "角色读取失败。")); setStatus(errorMessage(error, "角色读取失败。"), "error"); return false; }
      finally { if (refs.loadCharacters) refs.loadCharacters.disabled = state.busy; }
    }

    function runValues() {
      return {package_id: text(refs.runPackage && refs.runPackage.value), scope: state.scope,
        account_id: textInputValue(refs.accountID), account_username: textInputValue(refs.accountUsername),
        character_slot: text(refs.characterSlot && refs.characterSlot.value)};
    }

    function invalidatePreview() {
      state.runRequest += 1;
      state.preview = null;
      if (refs.previewPanel) refs.previewPanel.hidden = true;
      if (refs.confirm) { refs.confirm.disabled = true; refs.confirm.textContent = "确认发放"; }
      setText(refs.previewWarning, ""); clear(refs.previewStats); clear(refs.previewSkipped);
    }

    function renderPreview(payload) {
      const summary = previewSummary(payload);
      if (refs.previewPanel) refs.previewPanel.hidden = false;
      clear(refs.previewStats);
      [["targets", "目标角色"], ["eligible", "可发放"], ["skipped", "跳过"]].forEach(function (pair) {
        const value = pair[0] === "skipped" ? summary.skipped.length : summary[pair[0]];
        const stat = makeElement(document, "div", "gift-preview-stat");
        append(stat, makeElement(document, "strong", "", value), makeElement(document, "small", "", pair[1]));
        if (refs.previewStats) refs.previewStats.appendChild(stat);
      });
      const targetCount = summary.targets;
      const scope = state.scope === "all" ? "全部角色" : "所选角色";
      setText(refs.previewWarning, "确认后将向 " + targetCount + " 个" + scope + "提交礼包；其中 " + summary.eligible + " 个可发放，" + summary.skipped.length + " 个跳过。容量不足会整名角色跳过。");
      clear(refs.previewSkipped);
      if (summary.skipped.length && refs.previewSkipped) {
        refs.previewSkipped.appendChild(makeElement(document, "h4", "", "跳过的角色（容量不足会整角色跳过）"));
        const list = makeElement(document, "ul");
        summary.skipped.forEach(function (entry) {
          const account = text(entry && (entry.account || entry.username || entry.account_id), "未知账号");
          const character = text(entry && (entry.character || entry.character_name || entry.character_slot), "未知角色");
          const reason = text(entry && entry.reason, "未满足发放条件");
          const line = makeElement(document, "li", "", account + " · " + character + "：" + reason);
          if (/capacity|容量|背包|inventory|warehouse|full|满/i.test(reason)) setClass(line, "capacity-warning", true);
          list.appendChild(line);
        });
        refs.previewSkipped.appendChild(list);
      }
      if (refs.confirm) {
        refs.confirm.disabled = !summary.previewToken || summary.eligible <= 0;
        refs.confirm.textContent = summary.previewToken && summary.eligible > 0 ? "确认发放（" + targetCount + " 个角色）" : "确认发放";
      }
    }

    async function previewRun() {
      const values = runValues();
      if (safeID(values.package_id) === "") { setStatus("请选择要发放的礼包。", "error"); return false; }
      if (values.scope === "single") {
        if (!positiveInteger(values.account_id, null) && !values.account_username) { setStatus("单个角色发放需要账号 ID 或账号名。", "error"); return false; }
        if (slotNumber(values.character_slot) === null) { setStatus("请选择有效的角色槽位。", "error"); return false; }
      }
      invalidatePreview();
      const requestID = ++state.runRequest;
      setBusy(true); setStatus("正在生成发放预览…");
      try {
        const payload = await request("/api/gift-preview", {method: "POST", body: buildGiftRunPayload(values)});
        if (requestID !== state.runRequest) return false;
        const summary = previewSummary(payload);
        state.preview = {payload: buildGiftRunPayload(values), result: payload, token: summary.previewToken};
        renderPreview(payload);
        if (summary.previewToken) {
          setStatus("预览已生成，请核对目标和跳过原因后确认。", "success");
        } else if (summary.eligible <= 0) {
          setStatus("预览已生成，但当前没有可发放角色；请核对跳过原因。", "success");
        } else {
          throw new Error("服务未返回预览令牌，请重新预览。");
        }
        return true;
      } catch (error) { setStatus(errorMessage(error, "预览失败。"), "error"); return false; }
      finally { setBusy(false); }
    }

    async function confirmRun() {
      if (!state.preview || !state.preview.token) return false;
      const summary = previewSummary(state.preview.result);
      const targetLabel = state.scope === "all" ? "全部角色" : "所选角色";
      if (!confirmDelete("确认向 " + summary.targets + " 个" + targetLabel + "发放礼包吗？\n可发放 " + summary.eligible + " 个，跳过 " + summary.skipped.length + " 个；容量不足会整名角色跳过。")) return false;
      const payload = Object.assign({}, state.preview.payload, {preview_token: state.preview.token});
      setBusy(true); setStatus("正在提交发放任务…");
      try {
        const response = await request("/api/gift-runs", {method: "POST", body: payload});
        const run = response && (response.run || response.gift_run || response);
        const runID = run && (run.id || run.run_id);
        setText(refs.runResult, runID ? "发放任务已创建，任务 ID：" + runID : "发放任务已创建。");
        setClass(refs.runResult, "error", false); invalidatePreview(); await loadRuns(); return true;
      } catch (error) { setText(refs.runResult, errorMessage(error, "发放任务提交失败。")); setClass(refs.runResult, "error", true); setStatus(errorMessage(error, "发放任务提交失败。"), "error"); return false; }
      finally { setBusy(false); }
    }

    function runStatusLabel(run) {
      const status = text(run && (run.status || run.phase), "未知");
      return RUN_STATUS_LABELS[status] || status;
    }

    function localDateTime(value) {
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? text(value) : date.toLocaleString("zh-CN", {hourCycle: "h23"});
    }

    function renderRuns() {
      if (!refs.runs) return;
      clear(refs.runs);
      if (!state.runs.length) { refs.runs.appendChild(makeElement(document, "div", "empty", "暂无发放记录。")); return; }
      state.runs.forEach(function (run) {
        const id = run && (run.id || run.run_id); if (id === undefined || id === null) return;
        const row = makeElement(document, "article", "gift-run-row");
        const copy = makeElement(document, "div", "gift-run-copy");
        const packageName = run.package_name || run.name || (run.package && run.package.name) || ("礼包 #" + text(run.package_id));
        append(copy,
          makeElement(document, "strong", "", packageName),
          makeElement(document, "small", "", "任务 #" + text(id) + " · " + ((run.target_scope || run.scope) === "all" ? "全部角色" : "单个角色") + (run.created_at ? " · " + localDateTime(run.created_at) : "")));
        const status = makeElement(document, "span", "gift-run-status", runStatusLabel(run));
        const detail = makeElement(document, "button", "button", "查看详情"); detail.type = "button";
        detail.addEventListener("click", function () { loadRunDetail(id); });
        append(row, copy, status, detail); refs.runs.appendChild(row);
      });
      if (refs.runsMore) refs.runsMore.hidden = !state.runsNextCursor;
    }

    async function loadRuns(options) {
      const appendPage = Boolean(options && options.append);
      if (state.runsLoading || appendPage && !state.runsNextCursor) return state.runs;
      state.runsLoading = true;
      const cursor = appendPage ? state.runsNextCursor : "";
      if (refs.runsMore) refs.runsMore.disabled = true;
      try {
        const payload = await request(pageURL("/api/gift-runs", cursor));
        const page = asArray(payload && (payload.runs || payload.results)).filter(function (run) { return run && (run.id !== undefined || run.run_id !== undefined); });
        state.runs = appendPage ? state.runs.concat(page) : page;
        state.runsNextCursor = text(payload && (payload.next_cursor || payload.nextCursor));
        renderRuns(); return state.runs;
      } catch (error) { if (refs.runs) { clear(refs.runs); refs.runs.appendChild(makeElement(document, "div", "empty", errorMessage(error, "发放记录加载失败。"))); } throw error; }
      finally { state.runsLoading = false; if (refs.runsMore) refs.runsMore.disabled = false; }
    }

    function deliveryStatusClass(status) {
      const value = text(status).toLowerCase();
      if (value === "ok" || value === "success" || value === "delivered" || value === "completed" || value === "applied") return "delivery-status-ok";
      if (value === "skipped" || value === "skip" || value === "skip_capacity") return "delivery-status-skipped";
      if (value === "failed" || value === "error") return "delivery-status-failed";
      return "";
    }

    function deliveryStatusLabel(status) {
      const value = text(status, "未知");
      return DELIVERY_STATUS_LABELS[value] || value;
    }

    function renderDeliveries() {
      if (!refs.deliveries) return;
      clear(refs.deliveries);
      if (!state.deliveries.length) {
        refs.deliveries.appendChild(makeElement(document, "p", "gift-empty-note", "暂无角色处理结果。"));
        if (refs.deliveriesMore) refs.deliveriesMore.hidden = true;
        return;
      }
      const table = makeElement(document, "table", "gift-delivery-table");
      const head = makeElement(document, "thead"); const header = makeElement(document, "tr");
      ["账号", "角色", "状态", "错误"].forEach(function (label) { header.appendChild(makeElement(document, "th", "", label)); }); head.appendChild(header);
      const body = makeElement(document, "tbody");
      state.deliveries.forEach(function (delivery) {
        const row = makeElement(document, "tr");
        const account = text(delivery.account_id || delivery.account || delivery.username, "—");
        const characterName = text(delivery.character_name || delivery.character, "");
        const slot = delivery.character_slot === undefined || delivery.character_slot === null ? "" : " #" + text(delivery.character_slot);
        const character = characterName ? characterName + slot : (slot ? slot.trim() : "—");
        const status = text(delivery.status, "未知");
        const statusCell = makeElement(document, "td", deliveryStatusClass(status), deliveryStatusLabel(status));
        append(row, makeElement(document, "td", "", account), makeElement(document, "td", "", character), statusCell, makeElement(document, "td", delivery.error ? "gift-delivery-error" : "", text(delivery.error, "")));
        body.appendChild(row);
      });
      append(table, head, body); refs.deliveries.appendChild(table);
      if (refs.deliveriesMore) refs.deliveriesMore.hidden = !state.deliveriesNextCursor;
    }

    async function loadRunDetail(id, options) {
      if (!refs.runDetail) return false;
      const appendPage = Boolean(options && options.append && state.detailRunID !== null && idKey(state.detailRunID) === idKey(id));
      if (!appendPage) {
        state.detailRunID = id; state.deliveries = []; state.deliveriesNextCursor = "";
        refs.runDetail.hidden = false; setText(refs.runDetailTitle, "发放详情 · 任务 #" + text(id)); renderDeliveries();
      }
      if (state.deliveriesLoading || appendPage && !state.deliveriesNextCursor) return false;
      const cursor = appendPage ? state.deliveriesNextCursor : "";
      state.deliveriesLoading = true;
      if (refs.deliveriesMore) refs.deliveriesMore.disabled = true;
      try {
        const payload = await request(pageURL("/api/gift-runs/" + encodeURIComponent(text(id)), cursor));
        const run = payload && (payload.run || payload.gift_run || {});
        const deliveries = asArray(payload && payload.deliveries);
        state.deliveries = appendPage ? state.deliveries.concat(deliveries) : deliveries;
        state.deliveriesNextCursor = text(payload && (payload.next_cursor || payload.nextCursor));
        refs.runDetail.hidden = false;
        const scope = run && (run.target_scope || run.scope);
        setText(refs.runDetailTitle, "发放详情 · 任务 #" + text(id) + (scope === "all" ? "（全部角色）" : "（单个角色）"));
        renderDeliveries(); return run;
      } catch (error) { setStatus(errorMessage(error, "发放详情加载失败。"), "error"); return false; }
      finally { state.deliveriesLoading = false; if (refs.deliveriesMore) refs.deliveriesMore.disabled = false; }
    }

    async function loadPackages() {
      const requestID = ++state.packageRequest;
      try {
        const payload = await request("/api/gift-packages");
        if (requestID !== state.packageRequest) return state.packages;
        state.packages = packageListFromResponse(payload); renderPackages(); return state.packages;
      } catch (error) { if (refs.packages) { clear(refs.packages); refs.packages.appendChild(makeElement(document, "div", "empty", errorMessage(error, "礼包加载失败。"))); } throw error; }
    }

    Object.assign(state, {
      refs: refs,
      request: request,
      loadPackages: loadPackages,
      loadRuns: loadRuns,
      searchCatalog: searchCatalog,
      savePackage: savePackage,
      deletePackage: deletePackage,
      editPackage: editPackage,
      resetEditor: resetEditor,
      loadCharacters: loadCharacters,
      previewRun: previewRun,
      confirmRun: confirmRun,
      loadRunDetail: loadRunDetail,
      invalidatePreview: invalidatePreview,
      renderPreview: renderPreview,
      renderPackages: renderPackages,
      renderRuns: renderRuns,
      currentDefinition: currentDefinition,
      buildPayload: function () { return buildGiftRunPayload(runValues()); }
    });

    if (refs.newPackage) refs.newPackage.addEventListener("click", resetEditor);
    if (refs.refresh) refs.refresh.addEventListener("click", function () { Promise.allSettled([loadPackages(), loadRuns()]); });
    if (refs.packageForm) refs.packageForm.addEventListener("submit", function (event) { event.preventDefault(); savePackage(); });
    if (refs.packageSave) refs.packageSave.addEventListener("click", savePackage);
    [[refs.itemSearch, "item", refs.itemQuery], [refs.petSearch, "pet", refs.petQuery]].forEach(function (row) {
      if (!row[0]) return;
      row[0].addEventListener("submit", function (event) { event.preventDefault(); searchCatalog(row[1], textInputValue(row[2])).catch(function () {}); });
    });
    if (refs.runForm) refs.runForm.addEventListener("submit", function (event) { event.preventDefault(); previewRun(); });
    if (refs.confirm) refs.confirm.addEventListener("click", confirmRun);
    if (refs.previewReset) refs.previewReset.addEventListener("click", invalidatePreview);
    if (refs.runsRefresh) refs.runsRefresh.addEventListener("click", function () { loadRuns().catch(function () {}); });
    if (refs.runsMore) refs.runsMore.addEventListener("click", function () { loadRuns({append: true}).catch(function () {}); });
    if (refs.runDetailClose) refs.runDetailClose.addEventListener("click", function () { if (refs.runDetail) refs.runDetail.hidden = true; });
    if (refs.deliveriesMore) refs.deliveriesMore.addEventListener("click", function () { if (state.detailRunID !== null) loadRunDetail(state.detailRunID, {append: true}).catch(function () {}); });
    if (refs.loadCharacters) refs.loadCharacters.addEventListener("click", function () { loadCharacters().catch(function () {}); });
    if (refs.runPackage) refs.runPackage.addEventListener("change", invalidatePreview);
    [refs.accountID, refs.accountUsername, refs.characterSlot].forEach(function (input) {
      if (input) { input.addEventListener("input", invalidatePreview); input.addEventListener("change", invalidatePreview); }
    });
    const scopeInputs = root.querySelectorAll ? root.querySelectorAll('input[name="gift-scope"]') : [];
    asArray(scopeInputs).forEach(function (input) {
      input.addEventListener("change", function () { if (input.checked) setScope(input.value); });
    });
    setScope("single"); renderSelected("item"); renderSelected("pet"); renderRuns(); renderPackageOptions();
    loadPackages().catch(function (error) { setStatus(errorMessage(error, "礼包加载失败。"), "error"); });
    loadRuns().catch(function (error) { if (!refs.status || !refs.status.textContent) setStatus(errorMessage(error, "发放记录加载失败。"), "error"); });
    return state;
  }

  return {
    init: init,
    normalizeDefinition: normalizeDefinition,
    normalizeGiftPackage: normalizeGiftPackage,
    normalizeCatalogPayload: normalizeCatalogPayload,
    definitionPayload: definitionPayload,
    buildGiftRunPayload: buildGiftRunPayload,
    previewSummary: previewSummary,
    requestURL: requestURL,
    pageURL: pageURL,
    deliveryStatusLabel: function (status) { return DELIVERY_STATUS_LABELS[text(status, "未知")] || text(status, "未知"); }
  };
}));
