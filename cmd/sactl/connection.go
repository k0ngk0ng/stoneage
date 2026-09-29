package main

import (
	"flag"
	"fmt"
	"net/url"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/sacli"
)

type connectionOptions map[string]string

var connectionFlags = []string{"transport", "address", "web-base-url", "server-id", "character", "map-directory"}

func isConnectionFlag(name string) bool {
	for _, flag := range connectionFlags {
		if name == "--"+flag {
			return true
		}
	}
	return false
}

func (options connectionOptions) bind(fs *flag.FlagSet) {
	for _, name := range connectionFlags {
		fs.Func(name, "override "+strings.ReplaceAll(name, "-", " "), func(value string) error { options[name] = value; return nil })
	}
}

func (options connectionOptions) apply(config *sacli.Config) error {
	for name, value := range options {
		switch name {
		case "address":
			config.Address = value
		case "web-base-url":
			config.WebBaseURL = value
		case "server-id":
			config.ServerID = value
		case "character":
			config.Character = value
		case "map-directory":
			config.MapDirectory = value
		}
	}
	if transport, ok := options["transport"]; ok {
		config.Transport = transport
	} else if _, ok := options["web-base-url"]; ok {
		config.Transport = "http"
	} else if _, ok := options["address"]; ok {
		config.Transport = "tcp"
	}
	if config.Transport == "web" {
		config.Transport = "http"
	}
	switch config.Transport {
	case "http":
		u, err := url.Parse(config.WebBaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("invalid --web-base-url: use an http/https URL without credentials, query or fragment")
		}
	case "tcp":
		if config.Address == "" {
			return fmt.Errorf("--address is required for TCP")
		}
	default:
		return fmt.Errorf("unknown transport %q (use http or tcp)", config.Transport)
	}
	return nil
}

func (options connectionOptions) args() []string {
	var args []string
	for _, name := range connectionFlags {
		if value, ok := options[name]; ok {
			args = append(args, "--"+name, value)
		}
	}
	return args
}
