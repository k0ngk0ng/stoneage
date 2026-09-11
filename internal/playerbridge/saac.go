package playerbridge

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

type SAAC struct{ Queue Queue }

func validAccount(account string) bool {
	if len(account) == 0 || len(account) > 31 {
		return false
	}
	for _, c := range account {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

func (s SAAC) Read(ctx context.Context, account string, slot int) ([]byte, error) {
	return s.call(ctx, "read", account, slot, nil, nil)
}
func (s SAAC) Write(ctx context.Context, account string, slot int, expected, replacement []byte) error {
	if len(expected) == 0 || len(replacement) == 0 {
		return fmt.Errorf("不能通过资产编辑创建或清空角色存档")
	}
	_, err := s.call(ctx, "write", account, slot, expected, replacement)
	return err
}

func (s SAAC) call(ctx context.Context, op, account string, slot int, expected, replacement []byte) ([]byte, error) {
	if !validAccount(account) || slot < 0 || slot > 1 {
		return nil, fmt.Errorf("账号或角色槽位无效")
	}
	if len(expected) > playerdata.MaxSaveSize || len(replacement) > playerdata.MaxSaveSize {
		return nil, fmt.Errorf("角色存档超出容量")
	}
	response, id, err := s.Queue.Exchange(ctx, func(id string) ([]byte, error) {
		var out bytes.Buffer
		fmt.Fprintf(&out, "version=1\nid=%s\nop=%s\naccount=%s\nslot=%d\n", id, op, account, slot)
		if op == "write" {
			fmt.Fprintf(&out, "expected-length=%d\nnew-length=%d\n", len(expected), len(replacement))
		}
		out.WriteString("---\n")
		out.Write(expected)
		out.Write(replacement)
		return out.Bytes(), nil
	})
	if err != nil {
		return nil, err
	}
	header, payload, ok := bytes.Cut(response, []byte("---\n"))
	if !ok {
		return nil, fmt.Errorf("%w: 无效账号服务响应", playerdata.ErrUnavailable)
	}
	fields, err := parseLines(header, false)
	if err != nil {
		return nil, err
	}
	if fields["id"] != id || fields["version"] != "1" || fields["op"] != op || fields["account"] != account {
		return nil, fmt.Errorf("%w: 账号服务响应身份不匹配", playerdata.ErrUnavailable)
	}
	if fields["status"] != "ok" {
		return nil, bridgeError(fields["code"])
	}
	if fields["slot"] != strconv.Itoa(slot) {
		return nil, fmt.Errorf("%w: 角色槽位响应不匹配", playerdata.ErrUnavailable)
	}
	length, err := strconv.Atoi(fields["length"])
	if err != nil || length < 0 || length > playerdata.MaxSaveSize {
		return nil, fmt.Errorf("%w: 角色长度响应无效", playerdata.ErrUnavailable)
	}
	if op == "read" && len(payload) != length {
		return nil, fmt.Errorf("%w: 存档响应不完整", playerdata.ErrUnavailable)
	}
	if op == "write" && (length != len(replacement) || len(payload) != 0) {
		return nil, fmt.Errorf("%w: 保存响应不完整", playerdata.ErrUnavailable)
	}
	return payload, nil
}

func bridgeError(code string) error {
	switch code {
	case "not_found":
		return playerdata.ErrNotFound
	case "conflict", "online", "stale_target", "stale_item", "stale_pet":
		return playerdata.ErrConflict
	case "target_not_online":
		return &Error{Code: code, Message: "角色当前不在线"}
	case "busy_battle":
		return &Error{Code: code, Message: "角色正在战斗，请结束战斗后再修改"}
	case "busy_trade":
		return &Error{Code: code, Message: "角色正在交易，请结束交易后再修改"}
	case "io", "save_failed":
		return fmt.Errorf("%w: 游戏服务保存失败，请刷新确认结果", playerdata.ErrUnavailable)
	case "mutation_applied_save_failed":
		return fmt.Errorf("%w: 游戏内已修改，但保存失败，请刷新检查，勿重复赠送", playerdata.ErrUnavailable)
	default:
		return &Error{Code: code, Message: "游戏服务拒绝操作（" + code + "）"}
	}
}

type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }

func parseLines(data []byte, percent bool) (map[string]string, error) {
	fields := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid bridge response field")
		}
		if _, ok := fields[key]; ok {
			return nil, fmt.Errorf("duplicate bridge response field %s", key)
		}
		if percent {
			var err error
			value, err = percentDecode(value)
			if err != nil {
				return nil, err
			}
		}
		fields[key] = value
	}
	return fields, nil
}
