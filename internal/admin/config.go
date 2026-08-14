package admin

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ConfigManager struct {
	Path string
}

// ConfigField is the safe, documented subset of setup.cf exposed by the web
// console. Paths, passwords, ports and process wiring remain file-only so a
// typo cannot strand the server or expose a secret.
type ConfigField struct {
	Key   string
	Label string
	Hint  string
	Kind  string
	Value string
	Min   int
	Max   int
	Group string
}

type ConfigGroup struct {
	Title  string
	Fields []ConfigField
}

type configFieldDefinition struct {
	Group   string
	Key     string
	Label   string
	Hint    string
	Kind    string
	Default string
	Min     int
	Max     int
}

// Keep this list deliberately explicit. setup.cf contains many legacy paths,
// passwords and protocol settings that must not be editable from a browser.
var configFieldDefinitions = []configFieldDefinition{
	{Group: "基础运行", Key: "runlevel", Label: "运行级别", Hint: "服务优先级范围 -20–19，通常保持 0。", Kind: "int", Default: "0", Min: -20, Max: 19},
	{Group: "基础运行", Key: "debuglevel", Label: "GMSV 调试级别", Hint: "0–3，数值越高日志越多。", Kind: "int", Default: "0", Min: 0, Max: 3},
	{Group: "基础运行", Key: "SAMEIPLOGIN", Label: "同 IP 登录上限", Hint: "0 表示不额外限制。", Kind: "int", Default: "0", Min: 0, Max: 100000},
	{Group: "基础运行", Key: "fdnum", Label: "连接文件描述符上限", Hint: "建议至少 64。", Kind: "int", Default: "128", Min: 16, Max: 4096},
	{Group: "基础运行", Key: "PLAYERNUM", Label: "玩家数量上限", Hint: "0 表示使用服务端默认策略。", Kind: "int", Default: "0", Min: 0, Max: 100000},
	{Group: "基础运行", Key: "othercharnum", Label: "角色数量上限", Hint: "影响 NPC/角色内存分配。", Kind: "int", Default: "10000", Min: 1, Max: 1000000},
	{Group: "基础运行", Key: "petnum", Label: "宠物数量上限", Hint: "影响宠物内存分配。", Kind: "int", Default: "100", Min: 1, Max: 100000},
	{Group: "基础运行", Key: "objnum", Label: "对象数量上限", Hint: "影响地图对象内存分配。", Kind: "int", Default: "12000", Min: 1, Max: 1000000},
	{Group: "基础运行", Key: "itemnum", Label: "物品数量上限", Hint: "影响物品内存分配。", Kind: "int", Default: "10000", Min: 1, Max: 1000000},
	{Group: "基础运行", Key: "battlenum", Label: "战斗数量上限", Hint: "影响同时进行的战斗数量。", Kind: "int", Default: "100", Min: 1, Max: 100000},
	{Group: "基础运行", Key: "enable_nu_flow_control", Label: "启用 NU 移动流控", Hint: "默认关闭；旧版实现可能耗尽预算导致掉线。", Kind: "bool", Default: "0"},

	{Group: "玩法开关", Key: "RIDEMODE", Label: "骑宠模式", Hint: "0 标准模式，1 骑证模式，2 非骑证模式，3 自由骑乘。", Kind: "int", Default: "1", Min: 0, Max: 3},
	{Group: "玩法开关", Key: "FUSIONBEIT", Label: "允许宠物融合", Hint: "0 关闭，1 开启。", Kind: "bool", Default: "1"},
	{Group: "玩法开关", Key: "ENEMYACTION", Label: "敌人行动倍率", Hint: "0 关闭；数值越大遇敌频率越低。", Kind: "int", Default: "1", Min: 0, Max: 100000},
	{Group: "玩法开关", Key: "FMPOINTPK", Label: "启用家族点数 PK", Hint: "0 关闭，1 开启。", Kind: "bool", Default: "1"},
	{Group: "玩法开关", Key: "SHOWVIP", Label: "VIP 显示方式", Hint: "0 不显示，1 家族中显示，2 名称中显示。", Kind: "int", Default: "1", Min: 0, Max: 2},
	{Group: "玩法开关", Key: "PMOVE", Label: "大瞬移所需点数", Hint: "-1 关闭；非负数为每次消耗点数。", Kind: "int", Default: "1", Min: -1, Max: 1000000},
	{Group: "玩法开关", Key: "PANNOUNCE", Label: "小喇叭所需点数", Hint: "-1 关闭；非负数为每次消耗点数。", Kind: "int", Default: "1", Min: -1, Max: 1000000},
	{Group: "玩法开关", Key: "CHARLOOPS", Label: "遇敌时间倍率", Hint: "0.1 秒 × 倍率；按服务端原始整数填写。", Kind: "int", Default: "1", Min: 0, Max: 1000000},
	{Group: "玩法开关", Key: "allowmanorpk", Label: "允许庄园 PK", Hint: "0 关闭，1 开启。", Kind: "bool", Default: "1"},
	{Group: "玩法开关", Key: "PETUP", Label: "允许宠物升级", Hint: "0 关闭，1 开启。", Kind: "bool", Default: "1"},
	{Group: "玩法开关", Key: "RIDELEVEL", Label: "骑乘最低等级", Hint: "达到此等级后才可骑乘。", Kind: "int", Default: "10", Min: 0, Max: 1000},
	{Group: "玩法开关", Key: "RIDEPETLEVEL", Label: "骑乘宠物最低等级", Hint: "达到此等级后才可骑乘。", Kind: "int", Default: "200", Min: 0, Max: 1000},

	{Group: "奖励与等级", Key: "GIVEVIPPOINT", Label: "VIP 点数奖励", Hint: "每次奖励的点数。", Kind: "int", Default: "100", Min: 0, Max: 1000000},
	{Group: "奖励与等级", Key: "ANGELPLAYERTIME", Label: "天使玩家时间", Hint: "按服务端原始单位填写。", Kind: "int", Default: "5000", Min: 0, Max: 100000000},
	{Group: "奖励与等级", Key: "ANGELPLAYERMUN", Label: "天使玩家数量", Hint: "同时生效的数量上限。", Kind: "int", Default: "10", Min: 0, Max: 100000},
	{Group: "奖励与等级", Key: "BATTLEGOLD", Label: "战斗金币奖励", Hint: "单次战斗基础金币。", Kind: "int", Default: "10", Min: 0, Max: 1000000},
	{Group: "奖励与等级", Key: "SKILLUPPOINT", Label: "升级技能点", Hint: "升级获得的技能点。", Kind: "int", Default: "3", Min: 0, Max: 100000},
	{Group: "奖励与等级", Key: "MAXLEVEL", Label: "最大等级", Hint: "修改后需重新确认经验曲线。", Kind: "int", Default: "160", Min: 1, Max: 1000},
	{Group: "奖励与等级", Key: "LEVEL", Label: "默认等级", Hint: "新角色或测试角色的默认等级。", Kind: "int", Default: "1", Min: 1, Max: 1000},
	{Group: "奖励与等级", Key: "CHARTRANS", Label: "角色转生次数", Hint: "允许的角色转生上限。", Kind: "int", Default: "5", Min: 0, Max: 100},
	{Group: "奖励与等级", Key: "PETTRANS", Label: "宠物转生次数", Hint: "-1 表示沿用服务端默认值。", Kind: "int", Default: "-1", Min: -1, Max: 100},
	{Group: "奖励与等级", Key: "battleexp", Label: "战斗经验倍率", Hint: "按服务端原始倍率填写。", Kind: "int", Default: "300", Min: 0, Max: 100000000},
	{Group: "奖励与等级", Key: "GOLD", Label: "初始金币", Hint: "新角色初始金币。", Kind: "int", Default: "1000000", Min: 0, Max: 2147483647},

	{Group: "时间与日志", Key: "walkinterval", Label: "移动间隔", Hint: "按服务端原始单位填写。", Kind: "int", Default: "2500", Min: 0, Max: 600000},
	{Group: "时间与日志", Key: "CAinterval", Label: "角色动作间隔", Hint: "按服务端原始单位填写。", Kind: "int", Default: "2500", Min: 0, Max: 600000},
	{Group: "时间与日志", Key: "CDinterval", Label: "角色删除间隔", Hint: "按服务端原始单位填写。", Kind: "int", Default: "2500", Min: 0, Max: 600000},
	{Group: "时间与日志", Key: "CharSaveinterval", Label: "角色保存间隔", Hint: "按服务端原始单位填写。", Kind: "int", Default: "86400", Min: 0, Max: 604800},
	{Group: "时间与日志", Key: "Onelooptime", Label: "主循环间隔", Hint: "按服务端原始单位填写。", Kind: "int", Default: "5", Min: 0, Max: 3600},
	{Group: "时间与日志", Key: "Petdeletetime", Label: "宠物清理时间", Hint: "按服务端原始单位填写。", Kind: "int", Default: "1800", Min: 0, Max: 604800},
	{Group: "时间与日志", Key: "Itemdeletetime", Label: "物品清理时间", Hint: "按服务端原始单位填写。", Kind: "int", Default: "1800", Min: 0, Max: 604800},
	{Group: "时间与日志", Key: "Golddeletetime", Label: "金币清理时间", Hint: "按服务端原始单位填写。", Kind: "int", Default: "3600", Min: 0, Max: 604800},
	{Group: "时间与日志", Key: "protocolreadfrequency", Label: "协议读取频率", Hint: "按服务端原始单位填写。", Kind: "int", Default: "3000", Min: 0, Max: 600000},
	{Group: "时间与日志", Key: "allowerrornum", Label: "允许错误次数", Hint: "超过后可能断开连接。", Kind: "int", Default: "100", Min: 0, Max: 100000},
	{Group: "时间与日志", Key: "loghour", Label: "日志保留小时", Hint: "按服务端原始单位填写。", Kind: "int", Default: "24", Min: 0, Max: 8760},
	{Group: "时间与日志", Key: "battledebugmsg", Label: "战斗调试日志", Hint: "生产环境建议关闭。", Kind: "bool", Default: "0"},
	{Group: "时间与日志", Key: "encodekey", Label: "启用协议编码", Hint: "0 关闭，1 开启。", Kind: "bool", Default: "1"},
	{Group: "时间与日志", Key: "erruser_down", Label: "错误用户断开", Hint: "0 关闭，1 开启。", Kind: "bool", Default: "1"},
}

var editableConfigKeys = func() map[string]struct{} {
	keys := make(map[string]struct{}, len(configFieldDefinitions))
	for _, field := range configFieldDefinitions {
		keys[field.Key] = struct{}{}
	}
	return keys
}()

var editableConfigOrder = func() []string {
	keys := make([]string, 0, len(configFieldDefinitions))
	for _, field := range configFieldDefinitions {
		keys = append(keys, field.Key)
	}
	return keys
}()

var configDefinitionByKey = func() map[string]configFieldDefinition {
	definitions := make(map[string]configFieldDefinition, len(configFieldDefinitions))
	for _, field := range configFieldDefinitions {
		definitions[field.Key] = field
	}
	return definitions
}()

func BuildConfigGroups(values map[string]string) []ConfigGroup {
	groups := make([]ConfigGroup, 0, 4)
	groupIndex := map[string]int{}
	for _, definition := range configFieldDefinitions {
		index, exists := groupIndex[definition.Group]
		if !exists {
			index = len(groups)
			groupIndex[definition.Group] = index
			groups = append(groups, ConfigGroup{Title: definition.Group})
		}
		value := values[definition.Key]
		if value == "" {
			value = definition.Default
		}
		groups[index].Fields = append(groups[index].Fields, ConfigField{
			Key: definition.Key, Label: definition.Label, Hint: definition.Hint,
			Kind: definition.Kind, Value: value, Min: definition.Min,
			Max: definition.Max, Group: definition.Group,
		})
	}
	return groups
}

func (manager ConfigManager) Load() (map[string]string, error) {
	values := map[string]string{}
	if manager.Path == "" {
		return values, errors.New("server config path is not configured")
	}
	if err := manager.validateFile(); err != nil {
		return values, err
	}
	file, err := os.Open(manager.Path)
	if err != nil {
		return values, fmt.Errorf("open server config: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		if _, editable := editableConfigKeys[key]; editable {
			values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return values, err
	}
	return values, nil
}

func (manager ConfigManager) Update(values map[string]string) error {
	if manager.Path == "" {
		return errors.New("server config path is not configured")
	}
	for key, value := range values {
		if _, ok := editableConfigKeys[key]; !ok {
			return fmt.Errorf("config key %q is not editable", key)
		}
		if err := validateConfigValue(key, value); err != nil {
			return err
		}
	}
	if err := manager.validateFile(); err != nil {
		return err
	}
	input, err := os.ReadFile(manager.Path)
	if err != nil {
		return fmt.Errorf("read server config: %w", err)
	}
	lines := strings.Split(string(input), "\n")
	found := map[string]bool{}
	for index, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value, exists := values[key]
		if !exists {
			continue
		}
		lines[index] = key + "=" + value
		found[key] = true
	}
	for _, key := range editableConfigOrder {
		if value, exists := values[key]; exists && !found[key] {
			lines = append(lines, key+"="+value)
		}
	}
	content := []byte(strings.Join(lines, "\n"))
	mode := os.FileMode(0o640)
	if info, statErr := os.Stat(manager.Path); statErr == nil {
		mode = info.Mode().Perm()
	}
	directory := filepath.Dir(manager.Path)
	temporary, err := os.CreateTemp(directory, ".setup.cf.*.tmp")
	if err != nil {
		return fmt.Errorf("create config temporary: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return fmt.Errorf("write config temporary: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync config temporary: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, manager.Path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func (manager ConfigManager) validateFile() error {
	info, err := os.Lstat(manager.Path)
	if err != nil {
		return fmt.Errorf("inspect server config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("server config must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return errors.New("server config is not a regular file")
	}
	return nil
}

func validateConfigValue(key, value string) error {
	definition, ok := configDefinitionByKey[key]
	if !ok {
		return fmt.Errorf("config key %q is not editable", key)
	}
	if definition.Kind == "bool" {
		if value != "0" && value != "1" {
			return fmt.Errorf("%s must be 0 or 1", key)
		}
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < definition.Min || parsed > definition.Max {
		return fmt.Errorf("%s must be between %d and %d", key, definition.Min, definition.Max)
	}
	return nil
}
