package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/sacli"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/term"
)

func initConfig(args []string) error {
	fs := flag.NewFlagSet("sactl init", flag.ContinueOnError)
	profile := fs.String("profile", "", "named player profile")
	path := fs.String("config", "", "configuration file (default: ~/.config/sactl/sactl.toml or XDG_CONFIG_HOME)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: sactl init [--config <file>]")
	}
	if *profile != "" {
		if *path != "" {
			return errors.New("--profile and --config cannot be combined")
		}
		p, _, err := sacli.ProfilePaths(*profile)
		if err != nil {
			return err
		}
		*path = p
	}
	if *path == "" {
		if env := strings.TrimSpace(os.Getenv("STONEAGE_SACTL_CONFIG")); env != "" {
			*path = env
		} else {
			paths := sacli.ConfigSearchPaths()
			*path = paths[len(paths)-1]
		}
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("请在交互终端中运行 sactl init")
	}

	return writeProfileConfig(*path, *profile, terminalPrompt, os.Stdout)
}

func writeInitialConfig(path string, ask func(string, bool) (string, error), out io.Writer) error {
	return writeProfileConfig(path, "", ask, out)
}

func writeProfileConfig(path, profile string, ask func(string, bool) (string, error), out io.Writer) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(absolute); err == nil {
		return fmt.Errorf("配置已存在，不会覆盖：%s", absolute)
	} else if !os.IsNotExist(err) {
		return err
	}
	config := sacli.DefaultConfig()
	if profile != "" {
		_, socket, err := sacli.ProfilePaths(profile)
		if err != nil {
			return err
		}
		config.SocketPath = socket
	}
	config.Transport = "http"
	config.WebBaseURL, err = ask("游戏网址 [https://sa.ichenj.com]：", false)
	if err != nil {
		return err
	}
	if config.WebBaseURL == "" {
		config.WebBaseURL = "https://sa.ichenj.com"
	}
	u, err := url.Parse(config.WebBaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("请输入有效的 http/https 游戏网址，不含账号、查询参数或片段")
	}
	config.Character, err = ask("角色名（留空则登录后选择）：", false)
	if err != nil {
		return err
	}
	data, err := toml.Marshal(struct {
		Socket    string `toml:"socket_path"`
		Transport string `toml:"transport"`
		URL       string `toml:"web_base_url"`
		Character string `toml:"character"`
	}{config.SocketPath, config.Transport, config.WebBaseURL, config.Character})
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	closeErr := file.Close()
	if err != nil {
		os.Remove(absolute)
		return err
	}
	if closeErr != nil {
		os.Remove(absolute)
		return closeErr
	}
	fmt.Fprintf(out, "配置已保存：%s\n此配置不保存账号密码。登录时交互输入，凭据仅保留在后台进程内存中。\n", absolute)
	// Use short commands when the normal config lookup will select this file.
	_, selected, lookupErr := sacli.LoadConfigPath("")
	selectedAbs, _ := filepath.Abs(selected)
	if profile != "" {
		fmt.Fprintf(out, "登录：sactl login --profile %s\n查看角色：sactl --profile %s chars\n选择角色：sactl --profile %s enter <角色名>\n", profile, profile, profile)
	} else if lookupErr == nil && selected != "" && selectedAbs == absolute {
		fmt.Fprintln(out, "登录：sactl login\n查看角色：sactl chars\n选择角色：sactl enter <角色名>")
	} else {
		fmt.Fprintf(out, "登录：sactl login --config %q\n查看角色：sactl --config %q chars\n选择角色：sactl --config %q enter <角色名>\n", absolute, absolute, absolute)
	}
	return nil
}

// terminalPrompt never buffers ahead into a password prompt.
func terminalPrompt(label string, secret bool) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("请在交互终端输入账号密码，不支持命令行明文密码")
	}
	fmt.Fprint(os.Stderr, label)
	if secret {
		data, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(data), err
	}
	var result []byte
	var b [1]byte
	for {
		if _, err := io.ReadFull(os.Stdin, b[:]); err != nil {
			return "", err
		}
		if b[0] == '\n' {
			return strings.TrimSpace(string(result)), nil
		}
		result = append(result, b[0])
	}
}
