package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/admin"
)

func (w *aiRuntimeWiring) CreateCommand(ctx context.Context, profileID, baseURL string) (admin.AILocalCommand, error) {
	if w == nil || w.remote == nil || w.runtimeImage == "" {
		return admin.AILocalCommand{}, admin.ErrAIRuntimeUnavailable
	}
	invitation, err := w.remote.Invite(ctx, profileID, baseURL)
	if err != nil {
		return admin.AILocalCommand{}, err
	}
	identity := sha256.Sum256([]byte(baseURL + "\x00" + profileID))
	name := "stoneage-ai-local-" + hex.EncodeToString(identity[:12])
	args := []string{
		"docker", "run", "--rm", "-d", "--init", "--name", name,
		"--cpus", "1", "--memory", "1g", "--memory-swap", "1g", "--pids-limit", "128",
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=64m",
		"--mount", "type=volume,source=" + name + ",target=/var/lib/stoneage-ai",
		"--entrypoint", "/usr/local/bin/stoneage-ai-worker", w.runtimeImage,
		"--endpoint", invitation.Endpoint, "--profile", profileID,
		"--enrollment-token", invitation.Token, "--start",
	}
	for i := range args {
		args[i] = quoteLocalCommandArg(args[i])
	}
	return admin.AILocalCommand{Command: strings.Join(args, " "), ExpiresAt: invitation.ExpiresAt}, nil
}

func quoteLocalCommandArg(arg string) string {
	if arg != "" {
		safe := true
		for i := 0; i < len(arg); i++ {
			c := arg[i]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || strings.ContainsRune("-._/:@+%=,", rune(c))) {
				safe = false
				break
			}
		}
		if safe {
			return arg
		}
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
}

func (w *aiRuntimeWiring) ExecutorStatus(ctx context.Context, profileID string) (admin.AIExecutorStatus, error) {
	if w == nil || w.remote == nil {
		return admin.AIExecutorStatus{}, admin.ErrAIRuntimeUnavailable
	}
	status, err := w.remote.Status(ctx, profileID)
	if err != nil {
		return admin.AIExecutorStatus{}, err
	}
	if status.WorkerID == "" {
		return admin.AIExecutorStatus{Location: "server", Connected: true, Message: "由生产服务器执行"}, nil
	}
	message := "本地执行器已连接"
	if !status.Online {
		message = "本地执行器已断开，请在本机重新运行命令"
	} else if status.StartError != "" {
		message = "本地执行器已连接，玩家启动失败，请查看异常原因后点击启动"
	} else if status.StartRequested {
		message = "本地执行器已连接，正在启动玩家"
	}
	view := admin.AIExecutorStatus{Location: "local", Connected: status.Online, Message: message}
	if !status.LastSeenAt.IsZero() {
		view.LastSeen = &status.LastSeenAt
	}
	return view, nil
}
