"use strict";

(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) {
    module.exports = api;
    return;
  }
  root.StoneAgePlayers = api;
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
  const LOCATION_LABELS = {inventory: "随身", warehouse: "仓库"};
  const KIND_LABELS = {item: "物品", pet: "宠物", pet_skill: "宠物技能"};
  const DEFAULT_GRANT_CAPACITIES = {
    item_inventory: 15,
    item_warehouse: 30,
    pet_inventory: 5,
    pet_warehouse: 15
  };
  const FIXED_POINT_KEYS = {vi: true, str: true, tou: true, dx: true};

  function valueText(value, fallback) {
    if (value === null || value === undefined || value === "") {
      return fallback === undefined ? "—" : fallback;
    }
    return String(value);
  }

  function finiteNumber(value, fallback) {
    const number = Number(value);
    return Number.isFinite(number) ? number : fallback;
  }

  function normalizeSlot(value) {
    const slot = finiteNumber(value, null);
    return slot === null ? null : Math.trunc(slot);
  }

  function catalogKind(value) {
    return value === "pet" || value === "pet_skill" ? value : "item";
  }

  function buildCatalogURL(kind, query, offset, limit) {
    const params = new URLSearchParams();
    params.set("kind", catalogKind(kind));
    params.set("q", query === undefined || query === null ? "" : String(query));
    params.set("offset", String(Math.max(0, Math.trunc(finiteNumber(offset, 0)))));
    params.set("limit", String(Math.max(1, Math.trunc(finiteNumber(limit, DEFAULT_LIMIT)))));
    return "/api/player-catalog?" + params.toString();
  }

  function graphicURL(graphicID, kind) {
    if (graphicID === null || graphicID === undefined || String(graphicID) === "") {
      return "";
    }
    const assetKind = kind === "character" ? "character" : kind === "pet" ? "pet" : "item";
    return "/api/player-assets/graphic/" + encodeURIComponent(String(graphicID)) + "?kind=" + encodeURIComponent(assetKind);
  }

  function characterURL(accountID, slot) {
    return "/api/accounts/" + encodeURIComponent(String(accountID)) + "/players/" + encodeURIComponent(String(slot));
  }

  function playersURL(accountID) {
    return "/api/accounts/" + encodeURIComponent(String(accountID)) + "/players";
  }

  function asArray(value) {
    return Array.isArray(value) ? value : [];
  }

  function makeElement(doc, tag, className, text) {
    const element = doc.createElement(tag);
    if (className) {
      element.className = className;
    }
    if (text !== undefined && text !== null) {
      element.textContent = String(text);
    }
    return element;
  }

  function clear(element) {
    while (element && element.firstChild) {
      element.removeChild(element.firstChild);
    }
  }

  function setText(element, text) {
    if (element) {
      element.textContent = text === undefined || text === null ? "" : String(text);
    }
  }

  function entryKind(entry, fallback) {
    const kind = entry && entry.kind;
    return kind === "pet" || kind === "pet_skill" || kind === "item" ? kind : fallback || "item";
  }

  function entryGraphicKind(entry) {
    return entryKind(entry) === "pet" ? "pet" : "item";
  }

  function entryTemplateID(entry) {
    if (!entry) {
      return "";
    }
    return entry.template_id === null || entry.template_id === undefined || entry.template_id === ""
      ? entry.id
      : entry.template_id;
  }

  function makeAssetPreview(doc, entry, kind) {
    const frame = makeElement(doc, "span", "asset-preview");
    const source = graphicURL(entry && (entry.graphic_id === undefined ? entry.graphicId : entry.graphic_id), kind);
    if (!source) {
      frame.classList.add("missing");
      return frame;
    }
    const image = doc.createElement("img");
    image.src = source;
    image.alt = valueText(entry && entry.name, "外观") + "外观";
    image.addEventListener("error", function () {
      if (image.parentNode === frame) {
        frame.removeChild(image);
      }
      frame.classList.add("missing");
    });
    frame.appendChild(image);
    return frame;
  }

  function errorMessage(error, fallback) {
    if (error && error.message) {
      return error.message;
    }
    return fallback || "请求失败，请稍后重试。";
  }

  function integerParts(value) {
    const text = String(value === undefined || value === null ? "" : value).trim();
    if (!/^[+-]?\d+$/.test(text)) {
      return null;
    }
    const negative = text.charAt(0) === "-";
    let digits = text.replace(/^[+-]/, "").replace(/^0+/, "");
    if (!digits) {
      digits = "0";
    }
    return {negative: negative && digits !== "0", digits: digits};
  }

  function integerNumber(value) {
    const parts = integerParts(value);
    if (!parts) {
      return null;
    }
    const number = Number((parts.negative ? "-" : "") + parts.digits);
    return Number.isSafeInteger(number) ? number : null;
  }

  // Character and pet vi/str/tou/dx values are stored as hundredths by the
  // game. Keep the conversion in string arithmetic so entering 12.34 always
  // produces the exact API value 1234. Item attributes use their own units.
  function formatFixedPoint(value) {
    const parts = integerParts(value);
    if (!parts) {
      return valueText(value, "");
    }
    const digits = parts.digits.length <= 2 ? parts.digits.padStart(3, "0") : parts.digits;
    const integerPart = digits.slice(0, -2) || "0";
    const fractionPart = digits.slice(-2).replace(/0+$/, "");
    return (parts.negative ? "-" : "") + integerPart + (fractionPart ? "." + fractionPart : "");
  }

  function parseFixedPoint(value) {
    const text = String(value === undefined || value === null ? "" : value).trim();
    const match = text.match(/^([+-]?)(\d+)(?:\.(\d{0,2}))?$/);
    if (!match) {
      return {error: "请输入整数或最多两位小数。"};
    }
    const whole = match[2].replace(/^0+/, "") || "0";
    const fraction = (match[3] || "").padEnd(2, "0");
    const digits = (whole + fraction).replace(/^0+/, "") || "0";
    const negative = match[1] === "-" && digits !== "0";
    const number = Number((negative ? "-" : "") + digits);
    if (!Number.isSafeInteger(number)) {
      return {error: "数值超出允许范围。"};
    }
    return {value: number};
  }

  function fixedPointAttribute(attribute, scope) {
    return (scope === "character" || scope === "pet") &&
      Boolean(attribute && FIXED_POINT_KEYS[attribute.key]);
  }

  function numberOrString(input) {
    const value = String(input.value === undefined ? "" : input.value).trim();
    if (input.dataset && input.dataset.fixedPoint === "true") {
      const converted = parseFixedPoint(value);
      if (converted.error) {
        return converted;
      }
      const min = input.dataset.min === "" ? null : integerNumber(input.dataset.min);
      const max = input.dataset.max === "" ? null : integerNumber(input.dataset.max);
      if (min !== null && converted.value < min || max !== null && converted.value > max) {
        return {error: "数值超出允许范围。"};
      }
      return converted;
    }
    if (input.dataset && input.dataset.numeric === "true") {
      const number = Number(value);
      if (!Number.isFinite(number)) {
        return {error: "请输入有效数字。"};
      }
      const min = input.dataset.min === "" ? null : finiteNumber(input.dataset.min, null);
      const max = input.dataset.max === "" ? null : finiteNumber(input.dataset.max, null);
      if (min !== null && number < min || max !== null && number > max) {
        return {error: "数值超出允许范围。"};
      }
      return {value: number};
    }
    return {value: value};
  }

  function init(document, window, fetchImplementation) {
    const root = document.getElementById("player-admin");
    if (!root) {
      return null;
    }
    const fetcher = fetchImplementation || (window && window.fetch ? window.fetch.bind(window) : null);
    if (!fetcher) {
      return null;
    }

    const accountID = root.dataset.accountId;
    const csrf = root.dataset.csrf || "";
    const characterSelect = document.getElementById("player-character");
    const refreshButton = document.getElementById("players-refresh");
    const loadState = document.getElementById("player-load-state");
    const onlineStatus = document.getElementById("player-online-status");
    const snapshotElement = document.getElementById("player-snapshot");
    const catalogForm = document.getElementById("player-catalog-search");
    const catalogKindSelect = document.getElementById("catalog-kind");
    const catalogQuery = document.getElementById("catalog-query");
    const catalogHint = document.getElementById("catalog-search-hint");
    const catalogResults = document.getElementById("catalog-results");
    const catalogSelection = document.getElementById("catalog-selection");
    const dialog = document.getElementById("player-dialog");
    const dialogPanel = dialog && dialog.querySelector(".player-dialog-panel");
    const dialogTitle = dialog && dialog.querySelector("#player-dialog-title");
    const dialogMessage = dialog && dialog.querySelector("#player-dialog-message");
    const dialogAccept = dialog && dialog.querySelector("[data-player-dialog-accept]");
    const dialogCancel = dialog && dialog.querySelectorAll("[data-player-dialog-cancel]");
    const editorDialog = document.getElementById("player-editor-dialog");
    const editorPanel = editorDialog && editorDialog.querySelector(".player-dialog-panel");
    const editorTitle = editorDialog && editorDialog.querySelector("#player-editor-dialog-title");
    const editorContent = editorDialog && editorDialog.querySelector("#player-editor-dialog-content");
    const editorCancel = editorDialog && editorDialog.querySelectorAll("[data-player-editor-cancel]");
    const editorClose = editorDialog && editorDialog.querySelector("button[data-player-editor-cancel]");

    if (!characterSelect || !snapshotElement || !catalogForm || !catalogResults || !catalogSelection || !dialog || !editorDialog || !editorContent) {
      return null;
    }

    const state = {
      accountID: accountID,
      csrf: csrf,
      characters: [],
      characterSlot: null,
      snapshot: null,
      loading: false,
      busy: false,
      characterRequest: 0,
      snapshotRequest: 0,
      catalogRequest: 0,
      catalogEntries: [],
      catalogTotal: 0,
      catalogLoading: false,
      catalogError: "",
      selectedEntry: null,
      dialogResolve: null,
      previousFocus: null,
      editorPossession: null,
      editorPreviousFocus: null
    };

    function setLoadState(message, kind) {
      setText(loadState, message);
      loadState.classList.toggle("error", kind === "error");
      loadState.classList.toggle("success", kind === "success");
    }

    function setOnlineStatus(online) {
      if (!onlineStatus) return;
      onlineStatus.classList.remove("ok", "off", "unknown");
      if (online === true) {
        onlineStatus.classList.add("ok");
        setText(onlineStatus, "在线");
      } else if (online === false) {
        onlineStatus.classList.add("off");
        setText(onlineStatus, "离线");
      } else {
        onlineStatus.classList.add("unknown");
        setText(onlineStatus, "未选择");
      }
    }

    function setBusy(busy) {
      state.busy = busy;
      root.classList.toggle("busy", busy);
      root.querySelectorAll("[data-player-mutate]").forEach(function (element) {
        element.disabled = busy;
      });
      editorContent.querySelectorAll("[data-player-mutate]").forEach(function (element) {
        element.disabled = busy;
      });
      const submit = catalogSelection.querySelector("[data-catalog-mutate]");
      if (submit) {
        submit.disabled = busy || submit.dataset.capacityEmpty === "true";
      }
    }

    async function request(path, options) {
      const requestOptions = Object.assign({headers: {Accept: "application/json"}}, options || {});
      requestOptions.headers = Object.assign({Accept: "application/json"}, requestOptions.headers || {});
      const response = await fetcher(path, requestOptions);
      let payload = null;
      try {
        payload = await response.json();
      } catch (ignore) {
        payload = null;
      }
      if (!response.ok) {
        const message = payload && payload.error ? String(payload.error) : "请求失败（" + response.status + "）";
        const error = new Error(message);
        error.status = response.status;
        error.payload = payload;
        throw error;
      }
      return payload || {};
    }

    function mutationRequest(target, action, payload) {
      const body = Object.assign({revision: target.revision, action: action}, payload || {});
      return request(characterURL(target.accountID, target.characterSlot), {
        method: "POST",
        headers: {"Content-Type": "application/json", "X-CSRF-Token": state.csrf},
        body: JSON.stringify(body)
      });
    }

    function openDialog(options) {
      if (state.dialogResolve) {
        return Promise.resolve(false);
      }
      setText(dialogTitle, options.title || "确认操作");
      setText(dialogMessage, options.message || "确认执行此操作？");
      setText(dialogAccept, options.confirmText || "确认");
      dialogAccept.classList.toggle("danger", options.danger === true);
      state.previousFocus = document.activeElement;
      dialog.hidden = false;
      document.body.classList.add("modal-open");
      dialogAccept.focus();
      return new Promise(function (resolve) {
        state.dialogResolve = resolve;
      });
    }

    function closeDialog(result) {
      if (!state.dialogResolve) {
        return;
      }
      const resolve = state.dialogResolve;
      state.dialogResolve = null;
      dialog.hidden = true;
      if (editorDialog.hidden) {
        document.body.classList.remove("modal-open");
      }
      dialogAccept.classList.remove("danger");
      const focus = state.previousFocus;
      state.previousFocus = null;
      if (focus && typeof focus.focus === "function") {
        focus.focus();
      }
      resolve(result === true);
    }

    function closePossessionEditor() {
      if (editorDialog.hidden) {
        return;
      }
      editorDialog.hidden = true;
      clear(editorContent);
      state.editorPossession = null;
      const focus = state.editorPreviousFocus;
      state.editorPreviousFocus = null;
      if (dialog.hidden) {
        document.body.classList.remove("modal-open");
      }
      if (focus && typeof focus.focus === "function") {
        focus.focus();
      }
    }

    function openPossessionEditor(possession) {
      if (!possession || state.busy) {
        return;
      }
      if (!editorDialog.hidden) {
        closePossessionEditor();
      }
      state.editorPossession = possession;
      state.editorPreviousFocus = document.activeElement;
      if (possession.kind === "character") {
        renderCharacterEditor();
        setText(editorTitle, "编辑角色");
      } else {
        renderPossessionEditor(possession);
        setText(editorTitle, (possessionKind(possession.kind) === "pet" ? "编辑宠物" : "编辑物品"));
      }
      editorDialog.hidden = false;
      document.body.classList.add("modal-open");
      if (editorClose && typeof editorClose.focus === "function") {
        editorClose.focus();
      } else if (editorPanel && typeof editorPanel.focus === "function") {
        editorPanel.focus();
      }
    }

    function showMutationError(error) {
      if (error && error.status === 409) {
        openDialog({
          title: "数据已变化",
          message: "这个角色的资产刚刚发生变化，需要重新加载后再操作。",
          confirmText: "重新加载"
        }).then(function (reload) {
          if (reload) {
            loadSnapshot(state.characterSlot, true);
          }
        });
        setLoadState("数据已变化，请重新加载。", "error");
        return;
      }
      setLoadState(errorMessage(error, "保存失败，请稍后重试。"), "error");
    }

    async function performMutation(action, payload, options) {
      if (state.busy || !state.snapshot || state.characterSlot === null) {
        return;
      }
      const target = {
        accountID: state.accountID,
        characterSlot: state.characterSlot,
        snapshot: state.snapshot,
        revision: state.snapshot.revision
      };
      const accepted = await openDialog({
        title: options && options.title || "确认修改",
        message: options && options.message || "确认保存这项修改？",
        confirmText: options && options.confirmText || "保存",
        danger: options && options.danger === true
      });
      if (!accepted || state.busy) {
        return;
      }
      if (state.accountID !== target.accountID || state.characterSlot !== target.characterSlot || state.snapshot !== target.snapshot || !state.snapshot || state.snapshot.revision !== target.revision) {
        setLoadState("角色数据已变化，请重新加载后再试。", "error");
        return;
      }
      setBusy(true);
      setLoadState("正在保存…");
      try {
        const payloadResponse = await mutationRequest(target, action, payload);
        const nextSnapshot = payloadResponse && payloadResponse.snapshot ? payloadResponse.snapshot : payloadResponse;
        if (!nextSnapshot || typeof nextSnapshot !== "object") {
          throw new Error("服务返回的角色数据无效。");
        }
        closePossessionEditor();
        state.snapshot = nextSnapshot;
        renderSnapshot();
        setLoadState("已保存", "success");
      } catch (error) {
        showMutationError(error);
      } finally {
        setBusy(false);
      }
    }

    function renderCharacterOptions() {
      clear(characterSelect);
      if (!state.characters.length) {
        const option = makeElement(document, "option", "", "没有角色");
        option.value = "";
        characterSelect.appendChild(option);
        characterSelect.disabled = true;
        return;
      }
      state.characters.forEach(function (character) {
        const slot = normalizeSlot(character.slot);
        if (slot === null) {
          return;
        }
        const option = makeElement(document, "option");
        option.value = String(slot);
        option.textContent = "#" + slot + " · " + valueText(character.name, "未命名角色") + (character.online === true ? "（在线）" : "");
        characterSelect.appendChild(option);
      });
      characterSelect.disabled = false;
      if (state.characterSlot !== null && state.characters.some(function (character) { return normalizeSlot(character.slot) === state.characterSlot; })) {
        characterSelect.value = String(state.characterSlot);
      } else if (characterSelect.options.length) {
        state.characterSlot = normalizeSlot(characterSelect.options[0].value);
        characterSelect.value = String(state.characterSlot);
      }
    }

    function renderEmptySnapshot(message) {
      clear(snapshotElement);
      snapshotElement.appendChild(makeElement(document, "div", "empty", message));
    }

    function renderAttributeEditor(attribute, onSave, options) {
      const row = makeElement(document, "div", "attribute-row");
      const title = makeElement(document, "dt");
      title.appendChild(makeElement(document, "strong", "", valueText(attribute.label, attribute.key)));
      row.appendChild(title);
      const valueCell = makeElement(document, "dd");
      const form = makeElement(document, "form", "attribute-editor");
      const input = document.createElement("input");
      const fixedPoint = fixedPointAttribute(attribute, options && options.scope);
      input.type = "text";
      input.value = fixedPoint ? formatFixedPoint(attribute.value) : valueText(attribute.value, "");
      input.setAttribute("aria-label", valueText(attribute.label, attribute.key));
      input.dataset.fixedPoint = fixedPoint ? "true" : "false";
      const numeric = fixedPoint || typeof attribute.value === "number" || attribute.min !== undefined || attribute.max !== undefined;
      input.dataset.numeric = numeric ? "true" : "false";
      if (attribute.min !== undefined && attribute.min !== null) {
        input.dataset.min = String(attribute.min);
      }
      if (attribute.max !== undefined && attribute.max !== null) {
        input.dataset.max = String(attribute.max);
      }
      if (fixedPoint) {
        input.step = "0.01";
        input.setAttribute("inputmode", "decimal");
      }
      const bounds = [];
      if (attribute.min !== undefined && attribute.min !== null) {
        bounds.push("最小 " + (fixedPoint ? formatFixedPoint(attribute.min) : attribute.min));
      }
      if (attribute.max !== undefined && attribute.max !== null) {
        bounds.push("最大 " + (fixedPoint ? formatFixedPoint(attribute.max) : attribute.max));
      }
      if (bounds.length) {
        form.appendChild(makeElement(document, "small", "", bounds.join("，")));
      }
      form.insertBefore(input, form.firstChild);
      const button = makeElement(document, "button", "primary", "保存");
      button.type = "submit";
      button.dataset.playerMutate = "true";
      form.appendChild(button);
      form.addEventListener("submit", function (event) {
        event.preventDefault();
        if (state.busy) return;
        const converted = numberOrString(input);
        if (converted.error) {
          setLoadState(converted.error, "error");
          input.focus();
          return;
        }
        onSave(converted.value);
      });
      valueCell.appendChild(form);
      row.appendChild(valueCell);
      return row;
    }

    function possessionLocation(value) {
      return value === "warehouse" ? "warehouse" : "inventory";
    }

    function possessionKind(value) {
      return value === "pet" ? "pet" : "item";
    }

    function renderPetSkills(container, possession) {
      const skills = asArray(possession.skills);
      const section = makeElement(document, "div", "pet-skills");
      section.appendChild(makeElement(document, "span", "pet-skills-title", "宠物技能"));
      if (!skills.length) {
        section.appendChild(makeElement(document, "span", "muted", "暂无技能"));
        container.appendChild(section);
        return;
      }
      skills.forEach(function (skill) {
        const row = makeElement(document, "div", "skill-row");
        const empty = Number(skill.id) === -1;
        const label = "槽位 " + valueText(skill.slot, "?") + " · " + (empty ? "空位" : valueText(skill.name, "技能"));
        row.appendChild(makeElement(document, "span", "", label));
        if (!empty) {
          const button = makeElement(document, "button", "danger", "删除");
          button.type = "button";
          button.dataset.playerMutate = "true";
          button.addEventListener("click", function () {
            performMutation("delete_pet_skill", {
              location: possessionLocation(possession.location),
              slot: normalizeSlot(possession.slot),
              skill_slot: normalizeSlot(skill.slot)
            }, {
              title: "删除宠物技能",
              message: "确认删除「" + valueText(skill.name, "技能") + "」？",
              confirmText: "删除",
              danger: true
            });
          });
          row.appendChild(button);
        }
        section.appendChild(row);
      });
      container.appendChild(section);
    }

    function renderPossessionSummary(possession) {
      const kind = possessionKind(possession.kind);
      const location = possessionLocation(possession.location);
      const slot = normalizeSlot(possession.slot);
      const summary = makeElement(document, "div", "possession-summary");
      const header = makeElement(document, "div", "possession-card-header");
      header.appendChild(makeAssetPreview(document, possession, kind));
      const title = makeElement(document, "div", "possession-title");
      title.appendChild(makeElement(document, "strong", "", valueText(possession.name, kind === "pet" ? "未命名宠物" : "未命名物品")));
      if (kind === "pet") title.appendChild(makeElement(document, "small", "asset-level", levelLabel(possession)));
      header.appendChild(title);
      summary.appendChild(header);
      const meta = makeElement(document, "div", "possession-meta");
      meta.appendChild(makeElement(document, "span", "", (slot === null ? "槽位 —" : "槽位 " + slot)));
      meta.appendChild(makeElement(document, "span", "", slotDescription(kind, slot)));
      meta.appendChild(makeElement(document, "span", "", LOCATION_LABELS[location]));
      summary.appendChild(meta);
      return summary;
    }

    function possessionMutationAction(kind) {
      return kind === "pet" ? "set_pet" : "set_item";
    }

    function renderPossessionEditor(possession) {
      if (!editorContent) {
        return;
      }
      const kind = possessionKind(possession.kind);
      const location = possessionLocation(possession.location);
      const slot = normalizeSlot(possession.slot);
      clear(editorContent);
      editorContent.appendChild(renderPossessionSummary(possession));

      const nameForm = makeElement(document, "form", "possession-name-form");
      const nameInput = document.createElement("input");
      nameInput.type = "text";
      nameInput.maxLength = 64;
      nameInput.value = valueText(possession.name, "");
      nameInput.setAttribute("aria-label", "名称");
      const nameButton = makeElement(document, "button", "primary", "保存名称");
      nameButton.type = "submit";
      nameButton.dataset.playerMutate = "true";
      nameForm.appendChild(nameInput);
      nameForm.appendChild(nameButton);
      nameForm.addEventListener("submit", function (event) {
        event.preventDefault();
        if (!nameInput.value.trim()) {
          setLoadState("名称不能为空。", "error");
          nameInput.focus();
          return;
        }
        performMutation(possessionMutationAction(kind), {
          location: location,
          slot: slot,
          field: "name",
          name: nameInput.value.trim()
        }, {title: "修改名称", message: "确认把名称改为「" + nameInput.value.trim() + "」？"});
      });
      editorContent.appendChild(nameForm);

      const attributes = asArray(possession.attributes);
      if (attributes.length) {
        const list = makeElement(document, "div", "possession-attributes");
        attributes.forEach(function (attribute) {
          const row = makeElement(document, "div", "possession-attribute");
          row.appendChild(makeElement(document, "span", "", valueText(attribute.label, attribute.key)));
          row.appendChild(makeElement(document, "span", "", fixedPointAttribute(attribute, kind === "pet" ? "pet" : "item")
            ? formatFixedPoint(attribute.value)
            : valueText(attribute.value)));
          list.appendChild(row);
          const edit = renderAttributeEditor(attribute, function (value) {
            performMutation(possessionMutationAction(kind), {
              location: location,
              slot: slot,
              field: attribute.key,
              value: value
            }, {title: "修改属性", message: "确认修改「" + valueText(attribute.label, attribute.key) + "」？"});
          }, {scope: kind === "pet" ? "pet" : "item"});
          list.appendChild(edit);
        });
        editorContent.appendChild(list);
      } else {
        editorContent.appendChild(makeElement(document, "p", "muted", "没有可编辑属性。"));
      }
      if (kind === "pet") {
        renderPetSkills(editorContent, possession);
      }
      const actions = makeElement(document, "div", "possession-actions");
      const remove = makeElement(document, "button", "danger", "删除" + KIND_LABELS[kind]);
      remove.type = "button";
      remove.dataset.playerMutate = "true";
      remove.addEventListener("click", function () {
        performMutation("delete_" + kind, {location: location, slot: slot}, {
          title: "删除" + KIND_LABELS[kind],
          message: "确认删除「" + valueText(possession.name, KIND_LABELS[kind]) + "」？此操作无法撤销。",
          confirmText: "删除",
          danger: true
        });
      });
      actions.appendChild(remove);
      editorContent.appendChild(actions);
    }

    function renderPossessionCard(possession) {
      const kind = possessionKind(possession.kind);
      const location = possessionLocation(possession.location);
      const slot = normalizeSlot(possession.slot);
      const card = makeElement(document, "article", "possession-card");
      card.tabIndex = 0;
      card.setAttribute("role", "button");
      card.setAttribute("data-possession-edit", "true");
      card.setAttribute("aria-label", "编辑" + KIND_LABELS[kind] + "「" + valueText(possession.name, kind === "pet" ? "未命名宠物" : "未命名物品") + "」");
      card.appendChild(renderPossessionSummary(possession));
      card.addEventListener("click", function () {
        openPossessionEditor(possession);
      });
      card.addEventListener("keydown", function (event) {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          openPossessionEditor(possession);
        }
      });
      return card;
    }

    function slotDescription(kind, slot) {
      if (kind === "item" && slot !== null) {
        return slot < 5 ? "装备栏" : "背包";
      }
      if (kind === "pet") return "宠物栏";
      return "资产";
    }

    function renderPossessionKind(location, kind, entries, label) {
      const kindSection = makeElement(document, "div", "possession-kind");
      kindSection.appendChild(makeElement(document, "h5", "", label + " · " + entries.length + " 项"));
      if (entries.length) {
        const grid = makeElement(document, "div", "possession-grid");
        entries.forEach(function (possession) {
          grid.appendChild(renderPossessionCard(possession));
        });
        kindSection.appendChild(grid);
      } else {
        kindSection.appendChild(makeElement(document, "p", "muted", "暂无"));
      }
      return kindSection;
    }

    function renderPossessions(possessions) {
      const section = makeElement(document, "section", "player-section");
      const title = makeElement(document, "div", "player-section-title");
      title.appendChild(makeElement(document, "h3", "", "物品与宠物"));
      title.appendChild(makeElement(document, "small", "", possessions.length + " 项"));
      section.appendChild(title);
      if (!possessions.length) {
        section.appendChild(makeElement(document, "div", "empty", "当前没有物品或宠物。"));
        return section;
      }
      const groups = {inventory: {item: [], pet: []}, warehouse: {item: [], pet: []}};
      possessions.forEach(function (possession) {
        const location = possessionLocation(possession.location);
        const kind = possessionKind(possession.kind);
        groups[location][kind].push(possession);
      });
      ["inventory", "warehouse"].forEach(function (location) {
        const locationSection = makeElement(document, "div", "possession-location");
        locationSection.appendChild(makeElement(document, "h4", "", LOCATION_LABELS[location]));
        if (location === "inventory") {
          const equipment = groups.inventory.item.filter(function (possession) {
            const slot = normalizeSlot(possession.slot);
            return slot !== null && slot < 5;
          });
          const backpack = groups.inventory.item.filter(function (possession) {
            const slot = normalizeSlot(possession.slot);
            return slot === null || slot >= 5;
          });
          locationSection.appendChild(renderPossessionKind(location, "item", equipment, "装备栏"));
          locationSection.appendChild(renderPossessionKind(location, "item", backpack, "背包"));
          locationSection.appendChild(renderPossessionKind(location, "pet", groups.inventory.pet, "随身宠物"));
        } else {
          locationSection.appendChild(renderPossessionKind(location, "item", groups.warehouse.item, "仓库物品"));
          locationSection.appendChild(renderPossessionKind(location, "pet", groups.warehouse.pet, "仓库宠物"));
        }
        section.appendChild(locationSection);
      });
      return section;
    }

    function levelLabel(entry) {
      const level = asArray(entry && entry.attributes).find(function (attribute) { return attribute.key === "lv"; });
      return "等级 " + valueText(level && level.value);
    }

    function renderCharacterSummary() {
      const header = makeElement(document, "div", "possession-card-header");
      header.appendChild(makeAssetPreview(document, state.snapshot, "character"));
      const title = makeElement(document, "div", "possession-title");
      title.appendChild(makeElement(document, "strong", "", valueText(state.snapshot.name, "未命名角色")));
      title.appendChild(makeElement(document, "small", "asset-level", levelLabel(state.snapshot)));
      header.appendChild(title);
      return header;
    }

    function renderCharacterCard() {
      const card = makeElement(document, "article", "character-card");
      card.tabIndex = 0;
      card.setAttribute("role", "button");
      card.setAttribute("aria-label", "编辑角色「" + valueText(state.snapshot.name, "未命名角色") + "」");
      card.appendChild(renderCharacterSummary());
      const open = function () { openPossessionEditor({kind: "character"}); };
      card.addEventListener("click", open);
      card.addEventListener("keydown", function (event) {
        if (event.key === "Enter" || event.key === " ") { event.preventDefault(); open(); }
      });
      return card;
    }

    function renderCharacterEditor() {
      clear(editorContent);
      editorContent.appendChild(renderCharacterSummary());
      editorContent.appendChild(makeElement(document, "p", "muted", "角色槽位 " + valueText(state.characterSlot, "—") + " · " + (state.snapshot.online ? "当前在线" : "当前离线")));
      const attributesSection = makeElement(document, "section", "player-section");
      const attributesTitle = makeElement(document, "div", "player-section-title");
      attributesTitle.appendChild(makeElement(document, "h3", "", "金钱与角色属性"));
      attributesTitle.appendChild(makeElement(document, "small", "", "保存前会要求确认"));
      attributesSection.appendChild(attributesTitle);
      const attributes = asArray(state.snapshot.attributes);
      if (!attributes.length) {
        attributesSection.appendChild(makeElement(document, "div", "empty", "服务端没有返回角色属性。"));
      } else {
        const list = makeElement(document, "dl", "attribute-list");
        attributes.forEach(function (attribute) {
          list.appendChild(renderAttributeEditor(attribute, function (value) {
            performMutation("set_character", {field: attribute.key, value: value}, {
              title: "修改角色属性",
              message: "确认修改「" + valueText(attribute.label, attribute.key) + "」？"
            });
          }, {scope: "character"}));
        });
        attributesSection.appendChild(list);
      }
      editorContent.appendChild(attributesSection);
    }

    function renderSnapshot() {
      clear(snapshotElement);
      if (!state.snapshot) {
        setOnlineStatus(null);
        snapshotElement.appendChild(makeElement(document, "div", "empty", "选择角色后显示资产。"));
        renderCatalogSelection();
        return;
      }
      setOnlineStatus(state.snapshot.online === true ? true : state.snapshot.online === false ? false : null);
      snapshotElement.appendChild(renderCharacterCard());

      snapshotElement.appendChild(renderPossessions(asArray(state.snapshot.possessions)));
      renderCatalogSelection();
    }

    function renderCatalogEntry(entry, fallbackKind) {
      const normalized = Object.assign({}, entry, {kind: entryKind(entry, fallbackKind)});
      const button = makeElement(document, "button", "catalog-entry");
      button.type = "button";
      button.dataset.catalogEntry = "true";
      if (state.selectedEntry && String(entryTemplateID(state.selectedEntry)) === String(entryTemplateID(normalized)) && entryKind(state.selectedEntry) === entryKind(normalized)) {
        button.classList.add("selected");
      }
      button.appendChild(makeAssetPreview(document, normalized, entryGraphicKind(normalized)));
      const copy = makeElement(document, "span", "catalog-entry-copy");
      copy.appendChild(makeElement(document, "strong", "", valueText(normalized.name, "未命名")));
      copy.appendChild(makeElement(document, "small", "", KIND_LABELS[entryKind(normalized)]));
      button.appendChild(copy);
      button.addEventListener("click", function () {
        state.selectedEntry = normalized;
        renderCatalogResults();
        renderCatalogSelection();
      });
      return button;
    }

    function renderCatalogResults() {
      clear(catalogResults);
      if (state.catalogLoading) {
        catalogResults.appendChild(makeElement(document, "div", "empty", "正在搜索…"));
        return;
      }
      if (state.catalogError) {
        catalogResults.appendChild(makeElement(document, "div", "empty", state.catalogError));
        return;
      }
      if (!state.catalogEntries.length) {
        catalogResults.appendChild(makeElement(document, "div", "empty", "没有匹配结果。"));
        return;
      }
      const header = makeElement(document, "div", "catalog-results-header");
      header.appendChild(makeElement(document, "span", "", "找到 " + state.catalogTotal + " 项"));
      header.appendChild(makeElement(document, "span", "", "点击卡片预览"));
      catalogResults.appendChild(header);
      const grid = makeElement(document, "div", "catalog-grid");
      const kind = catalogKind(catalogKindSelect.value);
      state.catalogEntries.forEach(function (entry) {
        grid.appendChild(renderCatalogEntry(entry, kind));
      });
      catalogResults.appendChild(grid);
    }

    function petPossessions() {
      return state.snapshot ? asArray(state.snapshot.possessions).filter(function (possession) {
        return possessionKind(possession.kind) === "pet";
      }) : [];
    }

    function grantCapacityLimit(kind, location) {
      const key = kind + "_" + location;
      const capacities = state.snapshot && state.snapshot.capacities;
      const supplied = capacities && typeof capacities === "object" ? capacities[key] : undefined;
      return Math.max(0, Math.trunc(finiteNumber(supplied, DEFAULT_GRANT_CAPACITIES[key])));
    }

    function grantSlotRange(kind, location, capacity) {
      const first = kind === "item" && location === "inventory" ? 5 : 0;
      return {first: first, last: first + capacity};
    }

    function grantCapacity(kind, location) {
      const maximum = grantCapacityLimit(kind, location);
      const range = grantSlotRange(kind, location, maximum);
      const used = state.snapshot ? asArray(state.snapshot.possessions).filter(function (possession) {
        if (possessionKind(possession.kind) !== kind || possessionLocation(possession.location) !== location) {
          return false;
        }
        const slot = normalizeSlot(possession.slot);
        return slot !== null && slot >= range.first && slot < range.last;
      }).length : 0;
      return Math.max(0, maximum - used);
    }

    function renderCatalogSelection() {
      clear(catalogSelection);
      if (!state.selectedEntry) {
        catalogSelection.hidden = true;
        return;
      }
      catalogSelection.hidden = false;
      const entry = state.selectedEntry;
      const kind = entryKind(entry, catalogKind(catalogKindSelect.value));
      const heading = makeElement(document, "div", "catalog-selection-heading");
      heading.appendChild(makeAssetPreview(document, entry, entryGraphicKind(entry)));
      const headingCopy = makeElement(document, "div");
      headingCopy.appendChild(makeElement(document, "h3", "", valueText(entry.name, "未命名")));
      if (entry.description) {
        headingCopy.appendChild(makeElement(document, "p", "", entry.description));
      }
      heading.appendChild(headingCopy);
      catalogSelection.appendChild(heading);
      const form = makeElement(document, "form", "catalog-selection-form");
      if (kind === "pet_skill") {
        const pets = petPossessions();
        const targetLabel = makeElement(document, "label", "", "目标宠物");
        const target = document.createElement("select");
        target.name = "pet-slot";
        target.setAttribute("aria-label", "目标宠物");
        pets.forEach(function (pet) {
          const option = makeElement(document, "option");
          option.value = possessionLocation(pet.location) + ":" + normalizeSlot(pet.slot);
          option.textContent = LOCATION_LABELS[possessionLocation(pet.location)] + " · 槽位 " + normalizeSlot(pet.slot) + " · " + valueText(pet.name, "未命名宠物");
          target.appendChild(option);
        });
        if (!pets.length) {
          target.disabled = true;
          targetLabel.appendChild(target);
          form.appendChild(targetLabel);
          form.appendChild(makeElement(document, "p", "selection-note", "当前没有可设置技能的宠物。"));
        } else {
          targetLabel.appendChild(target);
          form.appendChild(targetLabel);
          const skillSlotLabel = makeElement(document, "label", "", "技能槽位");
          const skillSlot = document.createElement("input");
          skillSlot.type = "number";
          skillSlot.min = "0";
          skillSlot.max = "6";
          skillSlot.value = "0";
          skillSlot.required = true;
          skillSlot.className = "skill-slot-input";
          skillSlotLabel.appendChild(skillSlot);
          form.appendChild(skillSlotLabel);
          const submit = makeElement(document, "button", "primary", "确认设置技能");
          submit.type = "submit";
          submit.dataset.catalogMutate = "true";
          form.appendChild(submit);
          form.addEventListener("submit", function (event) {
            event.preventDefault();
            const selected = String(target.value).split(":");
            const skillSlotValue = normalizeSlot(skillSlot.value);
            if (selected.length !== 2 || skillSlotValue === null || skillSlotValue < 0 || skillSlotValue > 6) {
              setLoadState("请选择有效的宠物和技能槽位。", "error");
              return;
            }
            performMutation("set_pet_skill", {
              location: selected[0],
              slot: normalizeSlot(selected[1]),
              skill_slot: skillSlotValue,
              skill_id: entry.id === undefined ? entryTemplateID(entry) : entry.id
            }, {title: "设置宠物技能", message: "确认把「" + valueText(entry.name, "技能") + "」设置到所选宠物？"});
          });
        }
      } else {
        const locationLabel = makeElement(document, "label", "", "放置位置");
        const location = document.createElement("select");
        location.name = "location";
        ["inventory", "warehouse"].forEach(function (value) {
          const option = makeElement(document, "option");
          option.value = value;
          option.textContent = LOCATION_LABELS[value];
          location.appendChild(option);
        });
        locationLabel.appendChild(location);
        form.appendChild(locationLabel);
        const quantityLabel = makeElement(document, "label", "", "数量");
        const quantity = document.createElement("input");
        quantity.type = "number";
        quantity.name = "quantity";
        quantity.min = "1";
        quantity.max = "9999";
        quantity.value = "1";
        quantity.required = true;
        quantityLabel.appendChild(quantity);
        form.appendChild(quantityLabel);
        const note = makeElement(document, "p", "selection-note", "已选「" + valueText(entry.name, KIND_LABELS[kind]) + "」，赠送前请确认外观和数量。");
        form.appendChild(note);
        const submit = makeElement(document, "button", "primary", "确认赠送" + KIND_LABELS[kind]);
        submit.type = "submit";
        submit.dataset.catalogMutate = "true";
        form.appendChild(submit);
        function updateQuantityLimit() {
          const available = grantCapacity(kind, location.value === "warehouse" ? "warehouse" : "inventory");
          quantity.max = String(available);
          submit.dataset.capacityEmpty = available < 1 ? "true" : "false";
          submit.disabled = state.busy || available < 1;
          if (available > 0 && (normalizeSlot(quantity.value) === null || Number(quantity.value) > available)) {
            quantity.value = String(available);
          }
          setText(note, available > 0
            ? "已选「" + valueText(entry.name, KIND_LABELS[kind]) + "」，当前最多可放 " + available + " 个。"
            : "已选「" + valueText(entry.name, KIND_LABELS[kind]) + "」，当前位置没有空余槽位。");
        }
        location.addEventListener("change", updateQuantityLimit);
        updateQuantityLimit();
        form.addEventListener("submit", function (event) {
          event.preventDefault();
          const count = normalizeSlot(quantity.value);
          const available = grantCapacity(kind, location.value === "warehouse" ? "warehouse" : "inventory");
          if (count === null || count < 1 || count > available) {
            setLoadState(available > 0 ? "请输入 1 到 " + available + " 之间的数量。" : "当前位置没有空余槽位。", "error");
            quantity.focus();
            return;
          }
          const templateID = entryTemplateID(entry);
          performMutation(kind === "pet" ? "grant_pet" : "grant_item", {
            location: location.value === "warehouse" ? "warehouse" : "inventory",
            template_id: templateID,
            quantity: count
          }, {title: "赠送" + KIND_LABELS[kind], message: "确认赠送「" + valueText(entry.name, KIND_LABELS[kind]) + "」×" + count + "？"});
        });
      }
      catalogSelection.appendChild(form);
    }

    async function loadSnapshot(slot, force) {
      const normalizedSlot = normalizeSlot(slot);
      if (normalizedSlot === null) return;
      if (state.busy && !force) return;
      state.characterSlot = normalizedSlot;
      characterSelect.value = String(normalizedSlot);
      closePossessionEditor();
      const requestID = ++state.snapshotRequest;
      state.snapshot = null;
      setOnlineStatus(null);
      renderEmptySnapshot("正在加载角色资产…");
      renderCatalogSelection();
      setLoadState("正在加载角色资产…");
      try {
        const payload = await request(characterURL(state.accountID, normalizedSlot));
        if (requestID !== state.snapshotRequest) return;
        state.snapshot = payload && payload.snapshot ? payload.snapshot : payload;
        if (!state.snapshot || typeof state.snapshot !== "object") {
          throw new Error("服务返回的角色数据无效。");
        }
        renderSnapshot();
        setLoadState("已加载", "success");
      } catch (error) {
        if (requestID !== state.snapshotRequest) return;
        state.snapshot = null;
        renderEmptySnapshot(errorMessage(error, "角色资产加载失败。"));
        renderCatalogSelection();
        setLoadState(errorMessage(error, "角色资产加载失败。"), "error");
      }
    }

    async function loadCharacters() {
      const requestID = ++state.characterRequest;
      refreshButton.disabled = true;
      characterSelect.disabled = true;
      setLoadState("正在加载角色…");
      try {
        const payload = await request(playersURL(state.accountID));
        if (requestID !== state.characterRequest) return;
        state.characters = asArray(payload && payload.characters).filter(function (character) {
          return normalizeSlot(character && character.slot) !== null;
        });
        renderCharacterOptions();
        if (!state.characters.length) {
          state.snapshot = null;
          renderEmptySnapshot("这个账号没有角色。");
          setOnlineStatus(null);
          setLoadState("没有角色", "error");
          return;
        }
        await loadSnapshot(state.characterSlot);
      } catch (error) {
        if (requestID !== state.characterRequest) return;
        state.characters = [];
        renderCharacterOptions();
        state.snapshot = null;
        renderEmptySnapshot(errorMessage(error, "角色列表加载失败。"));
        setOnlineStatus(null);
        setLoadState(errorMessage(error, "角色列表加载失败。"), "error");
      } finally {
        if (requestID === state.characterRequest) {
          refreshButton.disabled = false;
        }
      }
    }

    async function searchCatalog(event) {
      if (event) event.preventDefault();
      if (state.catalogLoading) return;
      const requestID = ++state.catalogRequest;
      const kind = catalogKind(catalogKindSelect.value);
      const query = catalogQuery.value.trim();
      state.catalogLoading = true;
      state.catalogError = "";
      state.catalogEntries = [];
      state.catalogTotal = 0;
      state.selectedEntry = null;
      renderCatalogResults();
      try {
        const payload = await request(buildCatalogURL(kind, query, 0, DEFAULT_LIMIT));
        if (requestID !== state.catalogRequest) return;
        state.catalogEntries = asArray(payload && payload.entries);
        state.catalogTotal = finiteNumber(payload && payload.total, state.catalogEntries.length);
        state.catalogError = "";
        renderCatalogResults();
        renderCatalogSelection();
      } catch (error) {
        if (requestID !== state.catalogRequest) return;
        state.catalogError = errorMessage(error, "目录搜索失败。");
        renderCatalogResults();
        renderCatalogSelection();
      } finally {
        if (requestID === state.catalogRequest) {
          state.catalogLoading = false;
          renderCatalogResults();
        }
      }
    }

    characterSelect.addEventListener("change", function () {
      loadSnapshot(characterSelect.value);
    });
    refreshButton.addEventListener("click", function () {
      if (state.busy) return;
      if (state.characterSlot === null) {
        loadCharacters();
      } else {
        loadSnapshot(state.characterSlot, true);
      }
    });
    catalogForm.addEventListener("submit", searchCatalog);
    catalogKindSelect.addEventListener("change", function () {
      const kind = catalogKind(catalogKindSelect.value);
      state.catalogRequest += 1;
      state.catalogLoading = false;
      state.catalogError = "";
      state.catalogEntries = [];
      state.catalogTotal = 0;
      state.selectedEntry = null;
      renderCatalogResults();
      renderCatalogSelection();
      catalogQuery.placeholder = kind === "pet_skill" ? "技能名称或技能 ID" : "名称、ID 或描述";
      setText(catalogHint, kind === "pet_skill" ? "技能可按名称或 ID 搜索。" : "搜索结果可按名称、ID 或描述查找。");
    });
    dialogAccept.addEventListener("click", function () { closeDialog(true); });
    dialogCancel.forEach(function (button) { button.addEventListener("click", function () { closeDialog(false); }); });
    editorCancel.forEach(function (button) { button.addEventListener("click", function () { closePossessionEditor(); }); });
    document.addEventListener("keydown", function (event) {
      if (!dialog.hidden && event.key === "Escape") {
        event.preventDefault();
        closeDialog(false);
        return;
      }
      if (!editorDialog.hidden && event.key === "Escape") {
        event.preventDefault();
        closePossessionEditor();
      }
    });
    if (dialogPanel) {
      dialogPanel.addEventListener("keydown", function (event) {
        if (event.key === "Enter" && event.target === dialogPanel) {
          event.preventDefault();
          closeDialog(true);
        }
      });
    }
    if (editorPanel) {
      editorPanel.addEventListener("keydown", function (event) {
        if (event.key === "Enter" && event.target === editorPanel) {
          event.preventDefault();
          closePossessionEditor();
        }
      });
    }

    catalogQuery.placeholder = "名称、ID 或描述";
    loadCharacters();
    return state;
  }

  return {
    init: init,
    buildCatalogURL: buildCatalogURL,
    graphicURL: graphicURL,
    characterURL: characterURL,
    playersURL: playersURL,
    catalogKind: catalogKind,
    numberOrString: numberOrString,
    formatFixedPoint: formatFixedPoint,
    parseFixedPoint: parseFixedPoint,
    valueText: valueText
  };
}));
