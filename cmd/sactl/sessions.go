package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/sacli"
)

func (options *clientOptions) resolveProfile() error {
	if options.profile != "" || options.config != "" || options.socket != "" || os.Getenv("STONEAGE_SACTL_SOCKET") != "" {
		return nil
	}
	profile, err := sacli.SelectedProfile()
	if err != nil {
		return err
	}
	if profile != "default" {
		options.profile = profile
	}
	return nil
}

func sessionCommand(command string, args []string, asJSON bool) error {
	if command == "use" {
		if len(args) != 1 {
			return fmt.Errorf("usage: sactl use <profile|default>")
		}
		config, _, err := sacli.LoadClientProfileConfig(args[0], "")
		if err != nil {
			return err
		}
		if _, err = pingDaemon(config.SocketPath); err != nil {
			return fmt.Errorf("session %q is not running; use `sactl login --profile %s`", args[0], args[0])
		}
		if err = sacli.SelectProfile(args[0]); err != nil {
			return err
		}
		printResponse(sacli.Response{OK: true, Text: "active session: " + args[0]}, asJSON)
		return nil
	}
	if len(args) != 0 {
		return fmt.Errorf("usage: sactl sessions")
	}
	current, err := sacli.SelectedProfile()
	if err != nil {
		return err
	}
	type row struct {
		Profile   string
		Current   bool
		Running   bool
		Connected bool
		Account   string
		Character string
		Endpoint  string
	}
	rows := []row{}
	text := ""
	for _, profile := range sacli.KnownProfiles() {
		r := row{Profile: profile, Current: profile == current}
		config, _, err := sacli.LoadClientProfileConfig(profile, "")
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			response, err := sacli.Call(ctx, config.SocketPath, sacli.Request{Command: "status", JSON: true})
			cancel()
			if err == nil && response.OK {
				r.Running = true
				_ = json.Unmarshal(response.Data, &r)
			}
			r.Endpoint = config.Endpoint()
		}
		rows = append(rows, r)
		marker := " "
		if r.Current {
			marker = "*"
		}
		state := "offline"
		if r.Running {
			state = "logged out"
		}
		if r.Connected {
			state = "online"
		}
		text += fmt.Sprintf("%s %s\t%s\t%s\t%s\n", marker, profile, state, r.Account, r.Character)
	}
	data, _ := json.Marshal(rows)
	printResponse(sacli.Response{OK: true, Text: text, Data: data}, asJSON)
	return nil
}
