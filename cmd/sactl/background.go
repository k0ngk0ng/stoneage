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
	Version          string `json:"version"`
	IdleStop         bool   `json:"idle_stop"`
	ServerID         string `json:"server_id"`
	MapDirectory     string `json:"map_directory"`
	PID              int    `json:"pid"`
	InteractiveLogin bool   `json:"interactive_login"`
	Endpoint         string `json:"endpoint"`
	Transport        string `json:"transport"`
}

// login owns daemon lifecycle. An idle process from an older install or old
// endpoint can be replaced automatically; a connected account is preserved.
func prepareLoginDaemon(config sacli.Config, path, profile string) error {
	identity, err := pingDaemon(config.SocketPath)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		status, statusErr := sacli.Call(ctx, config.SocketPath, sacli.Request{Command: "status", JSON: true})
		cancel()
		var state struct {
			Connected bool
			Account   string
			Character string
		}
		if statusErr != nil || !status.OK || json.Unmarshal(status.Data, &state) != nil {
			return fmt.Errorf("cannot inspect existing session; refusing to replace it")
		}
		if state.Connected {
			return fmt.Errorf("already logged in as %s (%s); use `sactl login --profile <another-name>` for another session, or `sactl logout` first", state.Account, state.Character)
		}
		if !identity.InteractiveLogin || identity.Version != version || identity.Endpoint != config.Endpoint() || identity.Transport != config.Transport || identity.ServerID != config.ServerID || identity.MapDirectory != config.MapDirectory {
			ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
			command := "stop"
			if identity.IdleStop {
				command = "stop-if-idle"
			}
			stopped, stopErr := sacli.Call(ctx, config.SocketPath, sacli.Request{Command: command})
			cancel()
			if stopErr != nil || !stopped.OK {
				return fmt.Errorf("could not refresh idle session process")
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := pingDaemon(config.SocketPath); err != nil {
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("idle session process did not exit")
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
	}
	return startBackgroundMode(config, path, profile, os.Stderr, true)
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
	return startBackgroundMode(config, path, profile, out, false)
}

func startBackgroundMode(config sacli.Config, path, profile string, out io.Writer, interactive bool) error {
	if _, err := pingDaemon(config.SocketPath); err == nil {
		fmt.Fprintf(out, "sactl: daemon already running (%s)\n", config.SocketPath)
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"serve", "--foreground"}
	if interactive {
		args = append(args, "--interactive")
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	} else if path != "" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		args = append(args, "--config", absolute)
	}
	// Forward resolved non-secret settings, including CLI overrides. No config
	// file is needed and credentials are never placed on the process command line.
	args = append(args, "--socket", config.SocketPath, "--transport", config.Transport,
		"--address", config.Address, "--web-base-url", config.WebBaseURL,
		"--server-id", config.ServerID, "--map-directory", config.MapDirectory,
		"--character", config.Character)
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
