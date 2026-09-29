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
	if options.profile != "" && options.socket != "" {
		return errors.New("--profile and --socket cannot be combined")
	}
	if len(args) > 0 {
		return errors.New("usage: sactl [--profile <name>] login (account/password are prompted, never flags)")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("login requires an interactive terminal; credentials are not accepted in command arguments")
	}
	config, path, err := sacli.LoadClientProfileConfig(options.profile, options.config)
	if err != nil {
		return err
	}
	if err = options.connection.apply(&config); err != nil {
		return err
	}
	if options.socket != "" {
		config.SocketPath = options.socket
	}
	if err = prepareLoginDaemon(config, path, options.profile); err != nil {
		return err
	}
	identity, err := pingDaemon(config.SocketPath)
	if err != nil {
		return err
	}
	if !identity.InteractiveLogin {
		return errors.New("session process does not support interactive login")
	}
	if identity.Endpoint != config.Endpoint() || identity.Transport != config.Transport {
		return errors.New("session endpoint changed during login; retry login")
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
	if options.config == "" && options.socket == "" {
		if err := sacli.SelectProfile(options.profile); err != nil {
			return fmt.Errorf("logged in, but could not select session: %w", err)
		}
	}
	if character := options.connection["character"]; character != "" {
		response, err = sacli.Call(ctx, config.SocketPath, sacli.Request{Command: "enter", Args: []string{character}, Timeout: timeout, JSON: options.json})
		if err != nil {
			return err
		}
		if !response.OK {
			return fmt.Errorf("account is logged in, but character entry failed: %s; use `sactl chars` then `sactl enter <name|slot>`", response.Error)
		}
	}
	printResponse(response, options.json)
	return nil
}
