package admin

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/auth"
)

// auditEventView is the presentation model for the operator audit page. The
// stored event code and detail are kept alongside their human readable
// counterparts so an operator can understand a row without losing the exact
// record needed for troubleshooting.
type auditEventView struct {
	ID        int64
	ActorID   *int64
	Actor     string
	Event     string
	EventName string
	Username  string
	Target    string
	SourceIP  string
	Summary   string
	Detail    string
	HasDetail bool
	Severity  string
	CreatedAt time.Time
}

// auditEventViews converts persisted records at the HTTP boundary. Keeping
// this conversion out of auth.Store means the database and its audit history
// remain language neutral and append-only.
func auditEventViews(events []auth.AuditEvent) []auditEventView {
	views := make([]auditEventView, 0, len(events))
	for _, event := range events {
		views = append(views, humanizeAuditEvent(event))
	}
	return views
}

// humanizeAuditEvent creates the compact description shown in the table. It
// deliberately falls back to a generic description for future event types;
// new events therefore remain readable before a dedicated translation is
// added.
func humanizeAuditEvent(event auth.AuditEvent) auditEventView {
	parsed, isObject := auditDetailObject(event.Detail)
	detail := formatAuditDetail(event.Detail)
	name := auditEventName(event.Event)
	target := strings.TrimSpace(event.Username)
	if target == "" {
		target = auditTarget(event.Event, parsed)
	}
	summary := auditEventSummary(event.Event, event.Detail, parsed, isObject)
	if summary == "" {
		summary = "已记录此操作"
	}
	return auditEventView{
		ID:        event.ID,
		ActorID:   event.ActorID,
		Actor:     auditActor(event.ActorID),
		Event:     event.Event,
		EventName: name,
		Username:  event.Username,
		Target:    target,
		SourceIP:  strings.TrimSpace(event.SourceIP),
		Summary:   summary,
		Detail:    detail,
		HasDetail: strings.TrimSpace(event.Detail) != "",
		Severity:  auditSeverity(event.Event, parsed),
		CreatedAt: event.CreatedAt,
	}
}

func auditActor(id *int64) string {
	if id == nil || *id <= 0 {
		return "系统"
	}
	return "管理员 #" + strconv.FormatInt(*id, 10)
}

func auditDetailObject(detail string) (map[string]any, bool) {
	var value map[string]any
	if json.Unmarshal([]byte(detail), &value) != nil || value == nil {
		return nil, false
	}
	return value, true
}

func formatAuditDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	var value any
	if json.Unmarshal([]byte(detail), &value) == nil {
		formatted, err := json.MarshalIndent(value, "", "  ")
		if err == nil {
			return string(formatted)
		}
	}
	return detail
}

func auditEventName(event string) string {
	known := map[string]string{
		"account_created":                   "创建账号",
		"account_password_changed":          "修改账号密码",
		"account_status_changed":            "修改账号状态",
		"admin_created":                     "创建管理员",
		"admin_login_success":               "管理员登录成功",
		"admin_login_failed":                "管理员登录失败",
		"game_login_success":                "游戏登录成功",
		"game_login_failed":                 "游戏登录失败",
		"player_change_requested":           "提交角色修改",
		"player_changed":                    "角色修改成功",
		"player_change_failed":              "角色修改失败",
		"gift_package_created":              "创建礼包",
		"gift_package_updated":              "修改礼包",
		"gift_package_deleted":              "删除礼包",
		"gift_run_intent":                   "创建礼包发放任务",
		"gift_delivery_intent":              "准备发放礼包",
		"gift_delivery_applied":             "礼包发放成功",
		"gift_delivery_failed":              "礼包发放失败",
		"gift_delivery_skip_capacity":       "跳过礼包发放",
		"gift_delivery_uncertain":           "礼包发放结果待确认",
		"server_notification_sent":          "发送服务器通知",
		"server_notification_failed":        "发送服务器通知失败",
		"assets_sync_started":               "开始同步资源",
		"assets_sync_failed":                "资源同步失败",
		"ai_profile_created":                "创建 AI 玩家",
		"ai_profile_updated":                "修改 AI 玩家",
		"ai_profile_deleted":                "删除 AI 玩家",
		"ai_profile_provisioned":            "完成 AI 玩家初始化",
		"ai_profile_provision_failed":       "AI 玩家初始化失败",
		"ai_profile_provision_unavailable":  "AI 玩家初始化服务不可用",
		"ai_initialization_recovered":       "恢复 AI 玩家初始化",
		"ai_initialization_recovery_failed": "AI 玩家初始化恢复失败",
		"ai_unknown_review_failed":          "AI 未知回合核查失败",
		"ai_local_invitation_created":       "生成 AI 玩家本地运行命令",
	}
	if name, ok := known[event]; ok {
		return name
	}
	if strings.HasPrefix(event, "server_restarted_") {
		return "重启服务"
	}
	if strings.HasPrefix(event, "server_stopped_") {
		return "停止服务"
	}
	if strings.HasSuffix(event, "_config_changed") {
		return "修改服务配置"
	}
	if strings.HasPrefix(event, "ai_profile_") {
		return "AI 玩家操作"
	}
	if strings.HasPrefix(event, "gift_delivery_") {
		return "礼包发放记录"
	}
	return "系统事件"
}

func auditTarget(event string, detail map[string]any) string {
	if detail == nil {
		return ""
	}
	if value := auditString(detail, "character_name"); value != "" {
		return value
	}
	if value := auditString(detail, "account_username"); value != "" {
		return value
	}
	if value := auditString(detail, "profile_id"); value != "" {
		return "AI 玩家 " + value
	}
	if value := auditInt(detail, "package_id"); value != "" {
		return "礼包 #" + value
	}
	if strings.HasPrefix(event, "server_") {
		return auditServiceTarget(event)
	}
	return ""
}

func auditServiceTarget(event string) string {
	parts := strings.Split(event, "_")
	if len(parts) == 0 {
		return ""
	}
	target := parts[len(parts)-1]
	switch target {
	case "all":
		return "全部服务"
	case "game":
		return "游戏服务"
	case "gateway":
		return "游戏网关"
	case "gmsv":
		return "GMSV"
	case "saac":
		return "SAAC"
	default:
		return ""
	}
}

func auditEventSummary(event, raw string, detail map[string]any, isObject bool) string {
	if event == "ai_local_invitation_created" {
		return "已生成本地运行命令，等待在本机执行"
	}
	if event == "account_status_changed" {
		if strings.TrimSpace(raw) == "active" {
			return "账号已启用"
		}
		if strings.TrimSpace(raw) == "disabled" {
			return "账号已禁用"
		}
	}
	if event == "admin_login_failed" || event == "game_login_failed" {
		if strings.TrimSpace(raw) == "locked" {
			return "登录失败：账号已被临时锁定"
		}
		return "登录失败：账号或密码不正确"
	}
	if strings.HasPrefix(event, "server_restarted_") {
		return auditServiceAction(event, "已重启")
	}
	if strings.HasPrefix(event, "server_stopped_") {
		return auditServiceAction(event, "已停止")
	}
	if strings.HasSuffix(event, "_config_changed") {
		if strings.TrimSpace(raw) == "" {
			return "服务配置已更新"
		}
		return "服务配置已更新：" + truncateAuditText(raw, 180)
	}
	if event == "assets_sync_started" {
		return "资源同步任务已启动"
	}
	if event == "assets_sync_failed" {
		return auditFailureSummary(detail, raw, "资源同步失败")
	}
	if event == "server_notification_sent" {
		return "通知已发送"
	}
	if event == "server_notification_failed" {
		return auditFailureSummary(detail, raw, "通知发送失败")
	}
	if strings.HasPrefix(event, "gift_") {
		return giftAuditSummary(event, detail, raw, isObject)
	}
	if strings.HasPrefix(event, "player_") {
		return playerAuditSummary(event, detail, raw, isObject)
	}
	if strings.HasPrefix(event, "ai_") {
		return aiAuditSummary(event, detail, raw, isObject)
	}
	if isObject {
		return genericAuditObject(detail)
	}
	if strings.TrimSpace(raw) != "" {
		return "记录：" + truncateAuditText(raw, 180)
	}
	return "操作已完成"
}

func auditServiceAction(event, verb string) string {
	target := auditServiceTarget(event)
	if target == "" {
		return "服务" + verb
	}
	return target + verb
}

func giftAuditSummary(event string, detail map[string]any, raw string, isObject bool) string {
	if event == "gift_run_intent" {
		if isObject {
			return fmt.Sprintf("准备发放 %s 个目标，符合条件 %s 个，跳过 %s 个",
				auditIntOrZero(detail, "target_count"), auditIntOrZero(detail, "eligible"), auditIntOrZero(detail, "skipped"))
		}
		return "礼包发放任务已创建"
	}
	if event == "gift_delivery_intent" {
		return "已锁定发放对象，等待执行"
	}
	if strings.HasSuffix(event, "_applied") {
		return "礼包已写入角色存档"
	}
	if strings.HasSuffix(event, "_skip_capacity") {
		return auditFailureSummary(detail, raw, "本次发放已跳过")
	}
	if strings.HasSuffix(event, "_uncertain") {
		return auditFailureSummary(detail, raw, "发放结果暂不确定，请核对游戏状态")
	}
	if strings.HasSuffix(event, "_failed") {
		return auditFailureSummary(detail, raw, "礼包发放失败")
	}
	return "礼包操作已记录"
}

func playerAuditSummary(event string, detail map[string]any, raw string, isObject bool) string {
	if !isObject {
		return auditFailureSummary(detail, raw, "角色操作已记录")
	}
	action := auditStringNested(detail, "mutation", "action")
	if action == "" {
		action = auditString(detail, "action")
	}
	actionName := map[string]string{
		"set_character":    "修改人物属性",
		"grant_item":       "发放物品",
		"set_item":         "修改物品",
		"delete_item":      "删除物品",
		"grant_pet":        "发放宠物",
		"set_pet":          "修改宠物",
		"delete_pet":       "删除宠物",
		"set_pet_skill":    "修改宠物技能",
		"delete_pet_skill": "删除宠物技能",
		"grant_bundle":     "发放礼包内容",
	}[action]
	if actionName == "" {
		actionName = "角色数据"
	}
	if event == "player_change_failed" {
		return auditFailureSummary(detail, raw, actionName+"失败")
	}
	if event == "player_change_requested" {
		return "已提交" + actionName + "请求，等待游戏服务确认"
	}
	return actionName + "已完成"
}

func aiAuditSummary(event string, detail map[string]any, raw string, isObject bool) string {
	if event == "ai_unknown_review_failed" {
		return "未知回合核查未完成，玩家保持停止"
	}
	if strings.HasSuffix(event, "_failed") || strings.HasSuffix(event, "_unavailable") {
		return auditFailureSummary(detail, raw, "AI 操作未完成")
	}
	if event == "ai_profile_start" {
		return "已请求启动 AI 玩家"
	}
	if event == "ai_profile_pause" {
		return "已请求暂停 AI 玩家"
	}
	if event == "ai_profile_stop" {
		return "已请求停止 AI 玩家"
	}
	if isObject {
		parts := make([]string, 0, 3)
		if result := auditString(detail, "result"); result != "" {
			parts = append(parts, "结果："+auditResultLabel(result))
		}
		if version := auditInt(detail, "version"); version != "" {
			parts = append(parts, "版本 #"+version)
		}
		if value, ok := detail["key_changed"].(bool); ok && value {
			parts = append(parts, "已更新密钥")
		}
		if value, ok := detail["unlimited_funds"].(bool); ok && value {
			parts = append(parts, "无限金钱")
		}
		if len(parts) > 0 {
			return strings.Join(parts, "；")
		}
	}
	return "AI 操作已完成"
}

func auditFailureSummary(detail map[string]any, raw, fallback string) string {
	for _, key := range []string{"error", "reason", "message"} {
		if value := auditString(detail, key); value != "" {
			return fallback + "：" + truncateAuditText(value, 180)
		}
	}
	if strings.TrimSpace(raw) != "" && !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return fallback + "：" + truncateAuditText(raw, 180)
	}
	return fallback
}

func genericAuditObject(detail map[string]any) string {
	keys := make([]string, 0, len(detail))
	for key := range detail {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := auditValueText(detail[key])
		if value == "" {
			continue
		}
		parts = append(parts, auditKeyName(key)+"："+value)
		if len(parts) == 4 {
			break
		}
	}
	if len(parts) == 0 {
		return "已记录结构化详情"
	}
	return strings.Join(parts, "；")
}

func auditSeverity(event string, detail map[string]any) string {
	if strings.Contains(event, "failed") || strings.Contains(event, "uncertain") || strings.Contains(event, "error") {
		return "error"
	}
	if detail != nil && strings.EqualFold(auditString(detail, "result"), "failed") {
		return "error"
	}
	if strings.Contains(event, "requested") || strings.Contains(event, "intent") || strings.Contains(event, "started") {
		return "pending"
	}
	return "success"
}

func auditString(detail map[string]any, key string) string {
	if detail == nil || key == "" {
		return ""
	}
	value, ok := detail[key]
	if !ok {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func auditStringNested(detail map[string]any, parent, key string) string {
	value, ok := detail[parent].(map[string]any)
	if !ok {
		return ""
	}
	return auditString(value, key)
}

func auditInt(detail map[string]any, key string) string {
	if detail == nil {
		return ""
	}
	value, ok := detail[key]
	if !ok {
		return ""
	}
	switch number := value.(type) {
	case float64:
		if number == float64(int64(number)) {
			return strconv.FormatInt(int64(number), 10)
		}
	case json.Number:
		return number.String()
	case string:
		return strings.TrimSpace(number)
	}
	return ""
}

func auditIntOrZero(detail map[string]any, key string) string {
	if value := auditInt(detail, key); value != "" {
		return value
	}
	return "0"
}

func auditResultLabel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "accepted", "published", "succeeded", "success", "ok":
		return "成功"
	case "failed", "failure":
		return "失败"
	case "unavailable":
		return "不可用"
	case "not_accepted":
		return "未接受"
	default:
		return value
	}
}

func auditKeyName(key string) string {
	known := map[string]string{
		"account_id":      "账号编号",
		"character_slot":  "角色槽位",
		"code":            "错误代码",
		"default":         "默认模型",
		"eligible":        "符合条件",
		"error":           "错误",
		"key_changed":     "密钥变化",
		"package_id":      "礼包编号",
		"result":          "结果",
		"revision":        "版本",
		"scope":           "范围",
		"skipped":         "已跳过",
		"target_count":    "目标数量",
		"unlimited_funds": "无限金钱",
		"version":         "版本",
	}
	if value, ok := known[key]; ok {
		return value
	}
	return key
}

func auditValueText(value any) string {
	switch current := value.(type) {
	case nil:
		return ""
	case string:
		return truncateAuditText(strings.TrimSpace(current), 100)
	case bool:
		if current {
			return "是"
		}
		return "否"
	case float64:
		if current == float64(int64(current)) {
			return strconv.FormatInt(int64(current), 10)
		}
		return strconv.FormatFloat(current, 'f', 2, 64)
	default:
		return ""
	}
}

func truncateAuditText(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max-1]) + "…"
}
