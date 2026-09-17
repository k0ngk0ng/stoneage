package admin

// Startup labels are shared by lifecycle errors and executor status.
func AIStartStageLabel(value string) string {
	switch value {
	case "validate_factory":
		return "检查 Agent 配置"
	case "funding_policy":
		return "配置游戏资金策略"
	case "open_game_session":
		return "建立游戏会话"
	case "claim_local_gate":
		return "获取运行控制权"
	case "bind_game_identity":
		return "绑定游戏身份"
	case "build_game_backend":
		return "准备游戏能力"
	case "load_model":
		return "读取模型配置"
	case "register_game_capability":
		return "注册游戏接口"
	case "create_model_runner":
		return "准备模型执行器"

	case "profile":
		return "玩家配置"
	case "recovery":
		return "异常恢复"
	case "factory":
		return "会话工厂"
	case "session":
		return "游戏会话"
	case "runtime":
		return "运行时"
	case "validate_profile":
		return "校验玩家配置"
	case "load_binding":
		return "读取角色绑定"
	case "check_initial_state":
		return "检查初始状态"
	case "load_account":
		return "读取游戏账号"
	case "load_game_credential":
		return "读取游戏凭据"
	case "login_web_game":
		return "登录 Web 游戏"
	case "list_characters":
		return "读取角色列表"
	case "select_bound_character":
		return "选择绑定角色"
	case "enter_character_and_attach":
		return "进入角色并接管"
	case "activate_session":
		return "激活游戏会话"
	case "guard":
		return "启动前检查"
	case "start":
		return "启动执行"
	default:
		return ""
	}
}

func AIStartCodeLabel(value string) string {
	switch value {
	case "factory_invalid_config":
		return "Agent 配置无效"
	case "model_config_unavailable":
		return "模型配置不可用"
	case "model_credentials_unavailable":
		return "模型密钥不可用"
	case "runtime_provision_failed":
		return "运行环境准备失败"
	case "codex_unavailable":
		return "Codex 不可用"
	case "runner_config_invalid":
		return "执行器配置无效"
	case "runner_credentials_unavailable":
		return "执行器凭据不可用"
	case "funding_policy_failed":
		return "游戏资金策略失败"
	case "binding_load_failed":
		return "角色绑定读取失败"

	case "canceled":
		return "已取消"
	case "timeout":
		return "超时"
	case "closed":
		return "运行时已关闭"
	case "profile_not_found":
		return "玩家不存在"
	case "profile_changed":
		return "玩家配置已变化"
	case "profile_deleted":
		return "玩家已删除"
	case "profile_invalid":
		return "玩家配置无效"
	case "recovery_required":
		return "需要先恢复上一轮执行"
	case "recovery_failed":
		return "异常恢复失败"
	case "factory_unavailable":
		return "会话工厂不可用"
	case "factory_open_failed":
		return "会话创建失败"
	case "session_invalid":
		return "游戏会话无效"
	case "runtime_failed":
		return "运行时失败"
	case "attempt_pending":
		return "仍有执行待处理"
	case "guard_failed":
		return "启动前检查失败"
	case "start_failed":
		return "启动执行失败"
	case "provider_invalid_config":
		return "会话配置无效"
	case "binding_unavailable":
		return "角色绑定不可用"
	case "already_open":
		return "已有游戏会话"
	case "account_unavailable":
		return "游戏账号不可用"
	case "credentials_unavailable":
		return "游戏凭据不可用"
	case "character_unavailable":
		return "绑定角色不可用"
	case "game_session_unavailable":
		return "游戏会话不可用"
	case "session_open_failed":
		return "游戏会话建立失败"
	case "unknown":
		return "未知启动错误"
	default:
		return ""
	}
}
