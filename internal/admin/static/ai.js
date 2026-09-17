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

  const aiAuditEventNames = Object.freeze({
    "profile.created": "创建 AI 玩家配置",
    "profile.updated": "更新 AI 玩家配置",
    "profile.deleted": "删除 AI 玩家配置",
    "memory.confirmed": "确认长期记忆",
    "checkpoint.saved": "保存运行检查点",
    "decision.started": "开始模型决策",
    "decision.finished": "模型决策完成",
    "decision.failed": "模型决策失败",
    "budget.exceeded": "模型预算不足",
    "game.observation": "记录游戏观察",
    "schedule.created": "创建定时提醒",
    "schedule.claimed": "领取定时提醒",
    "schedule.completed": "完成定时提醒投递",
    "schedule.cancelled": "取消定时提醒",
    "ai_unknown_attempt_reviewed": "核查未知模型回合",
    "supervisor.turn_completed": "模型回合完成",
    "supervisor.turn_failed": "模型回合失败",
    "supervisor.no_progress": "检测到游戏没有进展",
    "supervisor.goal_completed": "游戏目标完成"
  });

  const aiAuditFieldNames = Object.freeze({
    account_id: "账号",
    character_id: "角色",
    status: "状态",
    model_config_id: "模型配置",
    personality: "人格",
    goal: "目标",
    skills: "Skills",
    unlimited_funds: "无限资金",
    daily_token_budget: "日 token 限额",
    external_spend_limit: "对外支出限额",
    initial_state: "初始状态",
    life: "生活策略",
    changed: "变更项"
  });

  function aiAuditDetailObject(detail) {
    return detail && typeof detail === "object" && !Array.isArray(detail) ? detail : {};
  }

  function aiAuditText(value) {
    return value === undefined || value === null ? "" : String(value);
  }

  function aiAuditFirstText(detail, keys) {
    for (const key of keys) {
      const value = detail[key];
      if (typeof value === "string" && value.trim()) return value.trim();
    }
    return "";
  }

  function aiAuditTokens(value) {
    const number = Number(value);
    if (!Number.isFinite(number)) return "";
    return number.toLocaleString("zh-CN") + " tokens";
  }

  function aiAuditTime(value) {
    if (!value) return "时间未知";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return aiAuditText(value);
    return date.toLocaleString("zh-CN");
  }

  function aiAuditDateTime(value) {
    if (!value) return "";
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "" : date.toISOString();
  }

  function aiAuditDuration(value) {
    const nanoseconds = Number(value);
    if (!Number.isFinite(nanoseconds) || nanoseconds <= 0) return "";
    let seconds = Math.round(nanoseconds / 1000000000);
    if (seconds < 60) return seconds + " 秒";
    const minutes = Math.floor(seconds / 60);
    seconds %= 60;
    if (minutes < 60) return minutes + " 分钟" + (seconds ? " " + seconds + " 秒" : "");
    const hours = Math.floor(minutes / 60);
    const remainingMinutes = minutes % 60;
    if (hours < 24) return hours + " 小时" + (remainingMinutes ? " " + remainingMinutes + " 分钟" : "");
    const days = Math.floor(hours / 24);
    const remainingHours = hours % 24;
    return days + " 天" + (remainingHours ? " " + remainingHours + " 小时" : "");
  }

  function aiAuditChangedFields(value) {
    if (!Array.isArray(value)) return "";
    return value.map(function (field) { return aiAuditFieldNames[field] || aiAuditText(field); }).filter(Boolean).join("、");
  }

  function aiAuditUnknownName(kind) {
    if (kind.indexOf("supervisor.") === 0) return "运行监督事件（" + kind + "）";
    if (kind.indexOf("game.") === 0) return "游戏事件（" + kind + "）";
    if (kind.indexOf("schedule.") === 0) return "定时任务事件（" + kind + "）";
    return "系统事件（" + kind + "）";
  }

  function describeAIEvent(event) {
    const kind = aiAuditText(event && event.kind).trim() || "unknown";
    const detail = aiAuditDetailObject(event && event.detail);
    const description = {
      name: aiAuditEventNames[kind] || aiAuditUnknownName(kind),
      action: "系统记录事件类型「" + kind + "」",
      result: "事件已记录",
      reasonLabel: "",
      reason: "",
      next: "",
      compact: kind === "checkpoint.saved",
      severity: "normal"
    };
    const id = aiAuditText(detail.schedule_id);
    const runAt = detail.run_at ? aiAuditTime(detail.run_at) : "";
    const nextRunAt = detail.next_run_at ? aiAuditTime(detail.next_run_at) : "";
    const scheduleName = aiAuditText(detail.kind || detail.title).trim();
    const scheduleTarget = scheduleName ? "「" + scheduleName + "」" : (id ? "「" + id + "」" : "");
    const attemptTokens = aiAuditTokens(detail.charged_tokens);
    switch (kind) {
      case "profile.created":
        description.action = "创建 AI 玩家配置";
        description.result = detail.version ? "配置已创建，版本 " + detail.version : "配置已创建";
        break;
      case "profile.updated":
        description.action = aiAuditChangedFields(detail.changed) ? "更新：" + aiAuditChangedFields(detail.changed) : "更新 AI 玩家配置";
        description.result = detail.version ? "配置已保存，版本 " + detail.version : "配置已保存";
        break;
      case "profile.deleted":
        description.action = "删除 AI 玩家配置";
        description.result = detail.version ? "配置已删除（最后版本 " + detail.version + "）" : "配置已删除";
        break;
      case "memory.confirmed":
        description.action = detail.subject ? "确认关于「" + detail.subject + "」的记忆" : "确认一条记忆";
        description.result = "已写入长期记忆";
        break;
      case "checkpoint.saved":
        description.action = "保存运行检查点";
        description.result = detail.version ? "检查点版本 " + detail.version + " 已保存" : "检查点已保存";
        break;
      case "decision.started":
        description.action = detail.reserved_tokens ? "预留 " + aiAuditTokens(detail.reserved_tokens) + "，开始一次模型决策" : "开始一次模型决策";
        description.result = "等待模型返回";
        break;
      case "decision.finished":
        description.action = "结算一次模型决策";
        description.result = attemptTokens ? "模型决策已完成并结算 " + attemptTokens : "模型决策已完成并结算";
        break;
      case "decision.failed":
        description.action = "结算一次失败的模型决策";
        description.result = attemptTokens ? "失败回合已结算 " + attemptTokens : "失败回合已结算";
        description.reasonLabel = "失败原因";
        description.reason = aiAuditFirstText(detail, ["error", "reason", "message"]) || "事件详情未记录失败原因";
        description.severity = "error";
        break;
      case "budget.exceeded":
        description.action = detail.requested_tokens ? "尝试预留 " + aiAuditTokens(detail.requested_tokens) : "尝试预留模型 token";
        description.result = "未启动模型决策：已超过每日 token 限额" + (detail.budget ? "（限额 " + aiAuditTokens(detail.budget) + "）" : "");
        description.reasonLabel = "说明";
        description.reason = detail.date ? "计费日期：" + detail.date : "本次请求被预算检查拦截";
        description.severity = "warning";
        break;
      case "game.observation":
        description.action = detail.kind ? "记录「" + detail.kind + "」游戏观察" : "记录一次游戏观察";
        if (detail.subject) description.action += "（" + detail.subject + "）";
        description.result = "观察已写入玩家记忆";
        break;
      case "schedule.created":
        description.action = "安排" + scheduleTarget + "定时提醒";
        description.result = runAt ? "已创建，计划于 " + runAt + " 处理" : "定时提醒已创建";
        const createdRepeat = aiAuditDuration(detail.repeat_interval_ns);
        if (createdRepeat) description.result += "，每 " + createdRepeat + "重复";
        break;
      case "schedule.claimed":
        description.action = "领取" + scheduleTarget + "定时提醒";
        description.result = runAt ? "已领取，原计划时间为 " + runAt + "，等待投递" : "已领取，等待投递";
        break;
      case "schedule.completed":
        description.action = "处理" + scheduleTarget + "定时提醒";
        if (detail.status === "pending") {
          description.result = nextRunAt ? "本次提醒已投递并保留，下一次为 " + nextRunAt : "本次提醒已投递并保留";
        } else {
          description.result = "定时提醒已投递完成";
        }
        break;
      case "schedule.cancelled":
        description.action = "取消" + scheduleTarget + "定时提醒";
        description.result = "定时提醒已取消";
        description.reasonLabel = "取消原因";
        description.reason = aiAuditFirstText(detail, ["reason"]) || "事件详情未记录取消原因";
        description.severity = "warning";
        break;
      case "ai_unknown_attempt_reviewed":
        description.action = detail.attempt_id ? "核查未知模型回合 " + detail.attempt_id : "核查未知模型回合";
        description.result = "核查已记录；模型结果仍保持未知";
        description.reasonLabel = "核查说明";
        description.reason = aiAuditFirstText(detail, ["reason"]) || "事件详情未记录核查说明";
        description.severity = "warning";
        break;
      case "supervisor.turn_completed":
        description.action = "监督器确认模型回合完成";
        description.result = detail.game_goal_completed === true ? "模型回合完成，游戏目标也已完成" : "模型回合完成，等待后续游戏进展";
        break;
      case "supervisor.turn_failed":
        description.action = "监督器处理一次失败的模型回合";
        description.result = detail.failure_count ? "第 " + detail.failure_count + " 次失败" : "模型回合失败";
        description.reasonLabel = "失败原因";
        description.reason = aiAuditFirstText(detail, ["error", "reason", "message"]) || "事件详情未记录失败原因";
        description.severity = "error";
        break;
      case "supervisor.no_progress":
        description.action = detail.count ? "记录第 " + detail.count + " 次无进展诊断" : "记录一次无进展诊断";
        description.result = detail.window_seconds ? "在 " + detail.window_seconds + " 秒窗口内未观察到游戏进展" : "未观察到游戏进展";
        description.reasonLabel = "处理";
        description.reason = "达到配置阈值后会暂停玩家；当前事件只表示已进行诊断";
        description.severity = "warning";
        break;
      case "supervisor.goal_completed":
        description.action = "监督器确认游戏目标完成";
        description.result = "游戏目标已完成，玩家运行被停止";
        break;
    }
    if (!description.next) description.next = aiAuditFirstText(detail, ["next_step", "next", "follow_up"]);
    return description;
  }

  function aiAuditJSON(value) {
    try { return JSON.stringify(value === undefined ? {} : value, null, 2); }
    catch (_) { return String(value); }
  }

  function aiAuditElement(tag, className, text) {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (text !== undefined) element.textContent = text;
    return element;
  }

  function aiAuditField(parent, label, text, className) {
    if (!text) return;
    const row = aiAuditElement("div", "ai-audit-field" + (className ? " " + className : ""));
    row.appendChild(aiAuditElement("dt", "ai-audit-label", label));
    row.appendChild(aiAuditElement("dd", "ai-audit-value", text));
    parent.appendChild(row);
  }

  function renderAIAuditEvents(parent, events) {
    const section = aiAuditElement("section", "ai-audit-events");
    section.appendChild(aiAuditElement("h3", "ai-audit-heading", "审计时间线"));
    section.appendChild(aiAuditElement("p", "muted ai-audit-hint", "最新事件在前；每条记录的原始 JSON 可展开查看。"));
    const timeline = aiAuditElement("div", "ai-audit-timeline");
    if (!Array.isArray(events) || !events.length) {
      timeline.appendChild(aiAuditElement("p", "empty", "暂无审计事件"));
      section.appendChild(timeline);
      parent.appendChild(section);
      return;
    }
    events.forEach(function (event) {
      const description = describeAIEvent(event);
      const item = aiAuditElement("article", "ai-audit-event ai-audit-event-" + description.severity + (description.compact ? " ai-audit-event-compact" : ""));
      const heading = aiAuditElement("div", "ai-audit-event-heading");
      const name = aiAuditElement("strong", "ai-audit-event-name", description.name);
      const time = aiAuditElement("time", "ai-audit-event-time", aiAuditTime(event.created_at));
      const dateTime = aiAuditDateTime(event.created_at);
      if (dateTime) time.dateTime = dateTime;
      heading.appendChild(name);
      heading.appendChild(time);
      item.appendChild(heading);
      if (event.actor) item.appendChild(aiAuditElement("p", "ai-audit-actor", "执行者：" + event.actor));
      if (description.compact) {
        item.appendChild(aiAuditElement("p", "ai-audit-compact-line", description.action + " · " + description.result));
      } else {
        const fields = aiAuditElement("dl", "ai-audit-fields");
        aiAuditField(fields, "动作 / 活动", description.action);
        aiAuditField(fields, "结果", description.result);
        if (description.reason) aiAuditField(fields, description.reasonLabel || "说明", description.reason, "ai-audit-reason");
        if (description.next) aiAuditField(fields, "下一步", description.next, "ai-audit-next");
        item.appendChild(fields);
      }
      const raw = aiAuditElement("details", "ai-audit-raw");
      raw.appendChild(aiAuditElement("summary", "", "查看原始 JSON"));
      raw.appendChild(aiAuditElement("pre", "ai-audit-raw-content", aiAuditJSON(event)));
      item.appendChild(raw);
      timeline.appendChild(item);
    });
    section.appendChild(timeline);
    parent.appendChild(section);
  }

  function renderAIAuditState(parent, state) {
    const section = aiAuditElement("section", "ai-audit-state");
    section.appendChild(aiAuditElement("h3", "ai-audit-heading", "生活记录"));
    section.appendChild(aiAuditElement("p", "muted ai-audit-hint", "私人笔记和定时提醒由 AI 自己维护，不能直接视为已确认的游戏事实。"));
    if (!state || state.available !== true) {
      section.appendChild(aiAuditElement("p", "empty", "生活记录暂不可用"));
      parent.appendChild(section);
      return;
    }
    const notes = Array.isArray(state.notes) ? state.notes : [];
    const schedules = Array.isArray(state.schedules) ? state.schedules : [];
    const notesLabel = "私人笔记（" + notes.length + " 条" + (state.notes_truncated ? "，仅显示最近 50 条" : "") + "）";
    const schedulesLabel = "定时提醒（" + schedules.length + " 条" + (state.schedules_truncated ? "，仅显示前 50 条，待处理提醒优先" : "") + "）";
    [
      {label: notesLabel, value: notes},
      {label: schedulesLabel, value: schedules}
    ].forEach(function (item) {
      const details = aiAuditElement("details", "ai-audit-state-details");
      details.appendChild(aiAuditElement("summary", "", item.label + " · 查看原始数据"));
      details.appendChild(aiAuditElement("pre", "ai-audit-raw-content", aiAuditJSON(item.value)));
      section.appendChild(details);
    });
    parent.appendChild(section);
  }

  const aiRuntimeStateNames = Object.freeze({
    starting: "启动中",
    running: "运行中",
    waiting: "等待下一次心跳",
    diagnosing: "检查游戏进展",
    paused: "已暂停",
    stopped: "已停止",
    completed: "已完成",
    error: "发生错误",
    unknown: "状态未知"
  });

  function aiRuntimeStateText(value) {
    const state = aiAuditText(value).trim().toLowerCase();
    return aiRuntimeStateNames[state] || (state ? state : aiRuntimeStateNames.unknown);
  }

  function aiRuntimeMessageText(value) {
    const message = aiAuditText(value).trim();
    if (!message) return "没有附加说明";
    const lower = message.toLowerCase();
    if (lower.indexOf("unresolved model turn") >= 0 || lower.indexOf("checkpoint_recovery_required") >= 0 || lower.indexOf("recovery") >= 0 || lower.indexOf("unknown") >= 0) {
      return "系统因上一轮模型回合结果未知而自动暂停，需要人工恢复确认";
    }
    if (lower.indexOf("no progress") >= 0 || lower.indexOf("没有进展") >= 0) {
      return "连续一段时间没有观察到游戏进展，运行时已暂停";
    }
    if (lower.indexOf("budget") >= 0 || lower.indexOf("token") >= 0) {
      return "模型额度已达到限制，运行时暂时暂停";
    }
    if (lower.indexOf("observation") >= 0 || lower.indexOf("observe") >= 0) {
      return "游戏状态观察失败，运行时正在重试或已暂停";
    }
    if (lower.indexOf("model") >= 0 || lower.indexOf("codex") >= 0) {
      return "模型回合发生错误，等待后续处理";
    }
    return message;
  }

  function aiRecoveryDetailText(response) {
    if (!response) return "";
    if (response.error) return aiAuditText(response.error.message || response.error);
    return "";
  }

  function aiRecoveryNextStep(status) {
    if (!status) return "没有待确认的异常回合；按需启动玩家即可。";
    const execution = status.execution || {};
    if (execution.container_stopped === false) {
      return "保持玩家停止，等待遗留模型容器退出后重新打开恢复确认。";
    }
    if (status.ready !== true) {
      return "保持玩家停止，先满足运行时停止和容器退出等前置条件，再重新打开恢复确认。";
    }
    return "先核对最近观测并勾选确认；提交后玩家仍保持停止，需要手动启动新轮次。";
  }

  function aiRecoveryActionSummary(status) {
    if (!status) return "没有待确认的异常回合。无需提交恢复确认；按需启动玩家即可。";
    const execution = status.execution || {};
    const lines = [];
    if (execution.container_stopped === false) {
      lines.push("恢复状态：待确认；遗留模型容器尚未退出。");
    } else if (execution.container_stopped === true) {
      lines.push("恢复状态：待确认；遗留模型容器已退出。");
    } else {
      lines.push("恢复状态：待确认；遗留模型容器状态无法确认。");
    }
    lines.push(status.ready === true ? "核查前置条件已满足。" : "核查前置条件尚未满足。");
    lines.push("下一步：" + aiRecoveryNextStep(status));
    return lines.join(" ");
  }

  function appendAIAuditRaw(parent, label, value) {
    const text = aiAuditText(value).trim();
    if (!text) return;
    const details = aiAuditElement("details", "ai-audit-raw");
    details.appendChild(aiAuditElement("summary", "", label));
    details.appendChild(aiAuditElement("pre", "ai-audit-raw-content", text));
    parent.appendChild(details);
  }

  function renderAIRecoverySummary(parent, profile, response) {
    const section = aiAuditElement("section", "ai-audit-recovery");
    section.appendChild(aiAuditElement("h3", "ai-audit-heading", "运行状态与恢复确认"));
    section.appendChild(aiAuditElement("p", "muted ai-audit-hint", "查看面板只读，不会提交恢复操作；状态来自最近一次服务端检查。"));
    const fields = aiAuditElement("dl", "ai-audit-fields");
    const runtime = profile && profile.runtime ? profile.runtime : {};
    aiAuditField(fields, "当前运行", aiRuntimeStateText(runtime.state));
    if (runtime.message || runtime.last_error) {
      aiAuditField(fields, "状态说明", aiRuntimeMessageText(runtime.message || runtime.last_error));
    }
    const recovery = response && response.recovery;
    if (recovery) {
      const execution = recovery.execution || {};
      aiAuditField(fields, "恢复状态", "待人工确认（旧回合结果未知）", "ai-audit-reason");
      aiAuditField(fields, "确认前置", recovery.ready === true ? "已满足，可以在核对后确认" : "未满足，暂不能提交确认", recovery.ready === true ? "ai-audit-next" : "ai-audit-reason");
      aiAuditField(fields, "遗留容器", execution.container_stopped === true ? "已退出" : execution.container_stopped === false ? "尚未退出" : "无法确认");
      aiAuditField(fields, "旧回合", recovery.attempt_id ? "回合 " + recovery.attempt_id + " · 最近检查 " + aiAuditTime(recovery.attempt_updated_at) : "身份未记录");
      aiAuditField(fields, "预留额度", recovery.reserved_tokens !== undefined ? aiAuditTokens(recovery.reserved_tokens) : "没有记录");
      aiAuditField(fields, "下一步", aiRecoveryNextStep(recovery), "ai-audit-next");
    } else if (response && response.error) {
      aiAuditField(fields, "恢复状态", "暂时无法读取待确认回合", "ai-audit-reason");
      aiAuditField(fields, "下一步", "保持玩家停止，稍后重新打开查看；不要重复提交未知请求。", "ai-audit-next");
    } else {
      aiAuditField(fields, "恢复状态", "没有待确认的异常回合");
      aiAuditField(fields, "下一步", "无需恢复确认；按需启动玩家即可。", "ai-audit-next");
    }
    section.appendChild(fields);
    const rawRuntime = aiAuditText(runtime.message || runtime.last_error).trim();
    if (rawRuntime) appendAIAuditRaw(section, "查看原始运行说明", rawRuntime);
    const rawRecovery = aiRecoveryDetailText(response);
    if (rawRecovery) appendAIAuditRaw(section, "查看恢复查询错误", rawRecovery);
    parent.appendChild(section);
  }

  function aiObservationContent(event) {
    const detail = aiAuditDetailObject(event && event.detail);
    return {kind: aiAuditText(detail.kind).trim(), subject: aiAuditText(detail.subject).trim(), content: aiAuditDetailObject(detail.content)};
  }

  function aiObservationAt(event) {
    return "最近观测 " + aiAuditTime(event && event.created_at) + "（不是实时状态）";
  }

  function aiObservationNumber(value) {
    if (value === null || value === undefined || typeof value === "boolean" || (typeof value === "string" && !value.trim())) return null;
    const number = Number(value);
    return Number.isFinite(number) ? number : null;
  }

  function aiObservationIsNewer(candidate, current) {
    if (!current) return true;
    const candidateTime = new Date(candidate && candidate.created_at).getTime();
    const currentTime = new Date(current && current.created_at).getTime();
    if (Number.isNaN(currentTime)) return !Number.isNaN(candidateTime);
    return !Number.isNaN(candidateTime) && candidateTime > currentTime;
  }

  function renderAIObservationEvidence(parent, events) {
    const section = aiAuditElement("section", "ai-audit-observation");
    section.appendChild(aiAuditElement("h3", "ai-audit-heading", "最近游戏状态（非实时）"));
    section.appendChild(aiAuditElement("p", "muted ai-audit-hint", "以下内容只来自已记录的服务端游戏观察；没有记录的字段明确标为无记录，不代表当前状态。"));
    const character = [], pets = new Map();
    let party = null;
    (Array.isArray(events) ? events : []).forEach(function (event) {
      if (!event || event.kind !== "game.observation") return;
      const observation = aiObservationContent(event);
      if (observation.kind === "character.level" && aiObservationNumber(observation.content.level) !== null) {
        character.push({event, observation});
      } else if (observation.kind === "pet.level" && observation.subject && aiObservationNumber(observation.content.level) !== null) {
        const previous = pets.get(observation.subject);
        if (!previous || aiObservationIsNewer(event, previous.event)) pets.set(observation.subject, {event, observation});
      } else if (observation.kind === "party.snapshot" && aiObservationIsNewer(event, party && party.event)) {
        party = {event, observation};
      }
    });
    character.sort((a, b) => new Date(b.event.created_at).getTime() - new Date(a.event.created_at).getTime());
    const fields = aiAuditElement("dl", "ai-audit-fields");
    if (character.length) {
      const current = character[0];
      aiAuditField(fields, "角色等级", aiObservationNumber(current.observation.content.level) + " 级 · " + aiObservationAt(current.event));
    } else {
      aiAuditField(fields, "角色等级", "无记录");
    }
    const petRows = Array.from(pets.values()).sort((a, b) => a.observation.subject.localeCompare(b.observation.subject));
    if (petRows.length) {
      aiAuditField(fields, "宠物等级", petRows.map(function (item) {
        return item.observation.subject + "：" + aiObservationNumber(item.observation.content.level) + " 级（" + aiObservationAt(item.event) + "）";
      }).join("；"));
    } else {
      aiAuditField(fields, "宠物", "无记录");
    }
    if (party && Array.isArray(party.observation.content.members)) {
      const members = party.observation.content.members;
      const memberText = members.length ? members.map(function (member) {
        const name = aiAuditText(member.name).trim() || aiAuditText(member.id).trim() || "未命名角色";
        const level = aiObservationNumber(member.level);
        const hp = aiObservationNumber(member.hp), maxHP = aiObservationNumber(member.max_hp);
        const parts = [name];
        if (level !== null && level > 0) parts.push(level + " 级");
        if (hp !== null && maxHP !== null && maxHP > 0) parts.push("HP " + hp + "/" + maxHP);
        return parts.join(" · ");
      }).join("；") : "已记录队伍为空";
      aiAuditField(fields, "已观测队伍", memberText + "（" + aiObservationAt(party.event) + "）");
      if (members.some(function (member) { return aiObservationNumber(member.hp) !== null; })) {
        aiAuditField(fields, "队伍 HP", "仅显示队伍观察中的 HP；角色自身 HP 没有单独记录（" + aiObservationAt(party.event) + "）");
      }
    } else {
      aiAuditField(fields, "队伍 / HP", "无记录");
    }
    aiAuditField(fields, "角色位置", "无记录");
    aiAuditField(fields, "战斗状态", "无记录");
    section.appendChild(fields);
    parent.appendChild(section);
  }

  window.StoneAgeAIAudit = {describe: describeAIEvent, render: renderAIAuditEvents, formatTime: aiAuditTime, renderRecovery: renderAIRecoverySummary, renderObservation: renderAIObservationEvidence};

  function initModels() {
    if (!modelRoot) return;
    const editor = document.getElementById("ai-model-editor");
    const form = document.getElementById("ai-model-form");
    const title = document.getElementById("ai-model-editor-title");
    const canWrite = modelRoot.dataset.canWrite === "true";
    const connectionTestAvailable = modelRoot.dataset.connectionTestAvailable === "true";
    const connectionTestUnavailableMessage = "模型测试服务尚未就绪，请检查运行环境配置。";
    let connectionTestRunning = false;
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
      button.disabled = !connectionTestAvailable || !canWrite || connectionTestRunning;
      button.setAttribute("aria-disabled", String(button.disabled));
      button.title = !canWrite ? "需要模型管理权限" : !connectionTestAvailable ? connectionTestUnavailableMessage : "直接向已保存 Base URL 的 Responses API 发送 hi";
    }

    document.querySelectorAll(".ai-test-model").forEach(function (button) {
      prepareConnectionTestButton(button);
      button.addEventListener("click", async function () {
        if (!connectionTestAvailable || !canWrite || connectionTestRunning || button.disabled) return;
        connectionTestRunning = true;
        document.querySelectorAll(".ai-test-model").forEach(prepareConnectionTestButton);
        const originalText = button.textContent;
        button.textContent = "测试中…";
        const name = button.dataset.modelName || "模型";
        message(modelRoot, "正在向「" + name + "」的 Responses API 发送 hi，请稍候…", false);
        try {
          const result = await api(modelRoot, "/api/ai/models/" + encodeURIComponent(button.dataset.id) + "/test", "POST", {});
          if (result.ok !== true) throw new Error("未收到有效测试结果，请刷新页面确认登录状态后重试。");
          const duration = Number.isFinite(result.duration_ms) ? "，耗时 " + (result.duration_ms / 1000).toFixed(1) + " 秒" : "";
          message(modelRoot, "「" + name + "」测试成功：已收到 Responses API 响应" + duration + "。", false);
        } catch (error) { message(modelRoot, "「" + name + "」" + error.message, true); }
        finally {
          connectionTestRunning = false;
          button.textContent = originalText;
          document.querySelectorAll(".ai-test-model").forEach(prepareConnectionTestButton);
        }
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

    function initializationCell(label, text, className) {
      const cell = document.createElement("td");
      if (className) cell.className = className;
      if (label) cell.dataset.label = label;
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
      const cell = initializationCell("", text, "empty");
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
        row.appendChild(initializationCell("玩家编号", item.profile_id || "—", "breakable"));
        row.appendChild(initializationCell("角色", item.character_name || "角色身份待核验"));
        row.appendChild(initializationCell("创建状态", initializationStatusLabel(item.status)));
        row.appendChild(initializationCell("更新时间", initializationUpdatedAt(item.updated_at)));
        const actions = document.createElement("td");
        actions.className = "ai-actions";
        actions.dataset.label = "操作";
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
          api(profileRoot, base + "/life-state", "GET"),
          api(profileRoot, base + "/recovery", "GET")
        ]);
        if (requestID !== auditRequest) return;
        if (results[0].status !== "fulfilled") throw results[0].reason;
        const state = results[1].status === "fulfilled" ? results[1].value : null;
        const recovery = results[2].status === "fulfilled" ? results[2].value : {error: results[2].reason};
        const events = results[0].value.events || [];
        heading.textContent = (profile.character && profile.character.name ? profile.character.name : id) + " · 只读生活记录与审计";
        content.replaceChildren();
        content.appendChild(aiAuditElement("p", "ai-audit-initial", initialText(profile)));
        renderAIRecoverySummary(content, profile, recovery);
        renderAIObservationEvidence(content, events);
        renderAIAuditState(content, state);
        renderAIAuditEvents(content, events);
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
          if (!recovery) {
            recoveryAck.checked = false;
            recoverySubmit.disabled = true;
            hide(recoveryPanel);
            message(profileRoot, "没有待确认的异常回合。", false);
            return;
          }
          recoveryAck.checked = false; recoverySubmit.disabled = true; recoveryMessage.textContent = "";
          const actionSummary = typeof aiRecoveryActionSummary === "function" ? aiRecoveryActionSummary(recovery) : (recovery.execution && recovery.execution.container_stopped === false ? "恢复状态：待确认；遗留模型容器尚未退出。" : recovery.ready ? "核查前置条件已满足。" : "核查前置条件尚未满足。");
          document.getElementById("ai-recovery-summary").textContent = "轮次 " + recovery.attempt_id + " · 保留预算 " + recovery.reserved_tokens + " tokens。" + actionSummary;
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
