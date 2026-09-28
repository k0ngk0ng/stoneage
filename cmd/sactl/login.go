package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/sacli"
	"golang.org/x/term"
)

func login(options clientOptions, args []string) error {
	if len(args) > 0 {
		return errors.New("usage: sactl [--profile <name>] login (account/password are prompted, never flags)")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("login requires an interactive terminal; credentials are not accepted in command arguments")
	}
	if options.socket != "" {
		return errors.New("login requires a profile or config, not --socket")
	}
	config, path, err := sacli.LoadProfileConfig(options.profile, options.config)
	if err != nil {
		return err
	}
	if path == "" {
		return errors.New("run sactl init first")
	}
	if err = startBackgroundTo(config, path, options.profile, os.Stderr); err != nil {
		return err
	}
	identity, err := pingDaemon(config.SocketPath)
	if err != nil {
		return err
	}
	if !identity.InteractiveLogin {
		return errors.New("running daemon is an older version; stop it and run login again")
	}
	if identity.Endpoint != config.Endpoint() || identity.Transport != config.Transport {
		return errors.New("running daemon uses a different server; stop this profile before changing servers")
	}
	account, err := terminalPrompt("游戏账号：", false)
	if err != nil {
		return err
	}
	password, err := terminalPrompt("游戏密码（隐藏输入，不保存到磁盘）：", true)
	if err != nil {
		return err
	}
	if strings.TrimSpace(account) == "" || password == "" {
		return errors.New("账号和密码不能为空")
	}
	timeout := options.timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	response, err := sacli.Call(ctx, config.SocketPath, sacli.Request{Command: "login", Args: []string{account, password}, Timeout: timeout})
	if err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("login failed: %s", response.Error)
	}
	printResponse(response, options.json)
	return nil
}
