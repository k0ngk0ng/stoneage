package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/sacli"
)

type daemonIdentity struct {
	PID              int    `json:"pid"`
	InteractiveLogin bool   `json:"interactive_login"`
	Endpoint         string `json:"endpoint"`
	Transport        string `json:"transport"`
}

func pingDaemon(socket string) (daemonIdentity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	response, err := sacli.Call(ctx, socket, sacli.Request{Command: "ping"})
	if err != nil {
		return daemonIdentity{}, err
	}
	if !response.OK {
		return daemonIdentity{}, fmt.Errorf("daemon rejected ping")
	}
	var identity daemonIdentity
	if len(response.Data) > 0 {
		err = json.Unmarshal(response.Data, &identity)
	}
	return identity, err
}

func startBackground(config sacli.Config, path, profile string) error {
	return startBackgroundTo(config, path, profile, os.Stdout)
}

func startBackgroundTo(config sacli.Config, path, profile string, out io.Writer) error {
	if _, err := pingDaemon(config.SocketPath); err == nil {
		fmt.Fprintf(out, "sactl: daemon already running (%s)\n", config.SocketPath)
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"serve", "--foreground"}
	if profile != "" {
		args = append(args, "--profile", profile)
	} else {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		args = append(args, "--config", absolute)
	}
	logPath := config.SocketPath + ".log"
	if err = os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(executable, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	detachProcess(cmd)
	if err = cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			return fmt.Errorf("daemon exited (%v); inspect %s", err, logPath)
		default:
		}
		identity, e := pingDaemon(config.SocketPath)
		if e == nil && identity.PID == cmd.Process.Pid {
			fmt.Fprintf(out, "sactl: background daemon ready (pid %d, %s %s)\nlog: %s\n", identity.PID, config.Transport, config.Endpoint(), logPath)
			return nil
		}
		time.Sleep(80 * time.Millisecond)
	}
	// Kill only the process created here, never an existing daemon.
	_ = cmd.Process.Kill()
	<-done
	return fmt.Errorf("daemon did not become ready; inspect %s", logPath)
}
