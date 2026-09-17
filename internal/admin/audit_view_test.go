package admin

import (
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/auth"
)

func TestHumanizeAuditEventKeepsRawDataAndShowsSummary(t *testing.T) {
	actor := int64(7)
	event := humanizeAuditEvent(auth.AuditEvent{
		ID:        11,
		ActorID:   &actor,
		Event:     "player_change_failed",
		Username:  "alice",
		SourceIP:  "192.0.2.10",
		Detail:    `{"character_slot":1,"mutation":{"action":"set_pet_skill"},"error":"宠物技能槽已占用"}`,
		CreatedAt: time.Unix(1700000000, 0),
	})

	if event.EventName != "角色修改失败" {
		t.Fatalf("event name = %q", event.EventName)
	}
	if event.Actor != "管理员 #7" || event.Target != "alice" {
		t.Fatalf("actor/target = %q/%q", event.Actor, event.Target)
	}
	if !strings.Contains(event.Summary, "修改宠物技能失败") || !strings.Contains(event.Summary, "宠物技能槽已占用") {
		t.Fatalf("summary = %q", event.Summary)
	}
	if !strings.Contains(event.Detail, "\n  \"mutation\"") || !event.HasDetail {
		t.Fatalf("formatted detail = %q has_detail=%v", event.Detail, event.HasDetail)
	}
	if event.Severity != "error" {
		t.Fatalf("severity = %q", event.Severity)
	}
}

func TestHumanizeAuditEventHandlesPlainAndUnknownDetails(t *testing.T) {
	locked := humanizeAuditEvent(auth.AuditEvent{Event: "admin_login_failed", Detail: "locked"})
	if locked.Summary != "登录失败：账号已被临时锁定" {
		t.Fatalf("locked summary = %q", locked.Summary)
	}

	unknown := humanizeAuditEvent(auth.AuditEvent{
		Event:  "future_event",
		Detail: `{"version":12,"result":"accepted","unexpected":"value"}`,
	})
	if unknown.EventName != "系统事件" || !strings.Contains(unknown.Summary, "版本：12") {
		t.Fatalf("unknown view = %#v", unknown)
	}
	if !strings.Contains(unknown.Detail, "\n  \"result\"") {
		t.Fatalf("unknown detail was not pretty printed: %q", unknown.Detail)
	}
}
