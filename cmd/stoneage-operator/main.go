// stoneage-operator is the small privileged boundary used by the web console
// for status and fixed restart actions. It accepts only fixed JSON actions over
// a Unix socket and never evaluates a command string supplied by the web process.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type request struct {
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
}

type status struct {
	Gateway  string `json:"gateway"`
	GMSV     string `json:"gmsv"`
	SAAC     string `json:"saac"`
	Database string `json:"database"`
}

type response struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Status status `json:"status,omitempty"`
}

type operator struct {
	packageRoot  string
	socket       string
	gatewayAddr  string
	upstreamAddr string
	saacAddr     string
	databasePath string
	restartMu    sync.Mutex
}

func main() {
	var value operator
	flag.StringVar(&value.socket, "socket", envOr("STONEAGE_OPERATOR_SOCKET", "/run/stoneage/operator.sock"), "Unix socket path")
	flag.StringVar(&value.packageRoot, "package-root", envOr("STONEAGE_PACKAGE_ROOT", "."), "Linux release package root")
	flag.StringVar(&value.gatewayAddr, "gateway", envOr("STONEAGE_GATEWAY_ADDRESS", "127.0.0.1:9065"), "gateway status address")
	flag.StringVar(&value.upstreamAddr, "upstream", envOr("STONEAGE_UPSTREAM_ADDRESS", "127.0.0.1:19065"), "GMSV status address")
	flag.StringVar(&value.saacAddr, "saac", envOr("STONEAGE_SAAC_ADDRESS", "127.0.0.1:9300"), "SAAC status address")
	flag.StringVar(&value.databasePath, "auth-db", envOr("STONEAGE_AUTH_DB", ""), "SQLite auth database path")
	flag.Parse()
	if err := value.listen(); err != nil {
		log.Fatal(err)
	}
}

func (value *operator) listen() error {
	if value.socket == "" {
		return errors.New("operator socket is empty")
	}
	if info, err := os.Lstat(value.socket); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("operator socket path must not be a symbolic link")
		}
		if info.IsDir() || info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("operator socket path is not a stale Unix socket")
		}
		if connection, dialErr := net.DialTimeout("unix", value.socket, 150*time.Millisecond); dialErr == nil {
			connection.Close()
			return fmt.Errorf("operator socket is already in use")
		}
		if err := os.Remove(value.socket); err != nil {
			return fmt.Errorf("remove stale operator socket: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(value.socket), 0o750); err != nil {
		return err
	}
	listener, err := net.Listen("unix", value.socket)
	if err != nil {
		return fmt.Errorf("listen operator socket: %w", err)
	}
	defer listener.Close()
	if err := os.Chmod(value.socket, 0o660); err != nil {
		return err
	}
	log.Printf("StoneAge operator listening on %s", value.socket)
	for {
		connection, err := listener.Accept()
		if err != nil {
			return err
		}
		go value.handle(connection)
	}
}

func (value *operator) handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(120 * time.Second))
	var input request
	if err := json.NewDecoder(connection).Decode(&input); err != nil {
		writeResponse(connection, response{Error: "invalid request: " + err.Error()})
		return
	}
	var output response
	switch input.Action {
	case "status":
		output.Status = value.status()
		output.OK = true
	case "restart", "restart_server":
		if err := value.restartScript("restart-server.sh"); err != nil {
			output.Error = err.Error()
		} else {
			output.OK = true
		}
	case "restart_game":
		if err := value.restartScript("restart-game.sh"); err != nil {
			output.Error = err.Error()
		} else {
			output.OK = true
		}
	case "restart_gmsv":
		if err := value.restartScript("restart-gmsv.sh"); err != nil {
			output.Error = err.Error()
		} else {
			output.OK = true
		}
	case "restart_saac":
		if err := value.restartScript("restart-saac.sh"); err != nil {
			output.Error = err.Error()
		} else {
			output.OK = true
		}
	case "stop", "stop_server":
		if err := value.restartScript("stop-server.sh"); err != nil {
			output.Error = err.Error()
		} else {
			output.OK = true
		}
	case "stop_game":
		if err := value.restartScript("stop-game.sh"); err != nil {
			output.Error = err.Error()
		} else {
			output.OK = true
		}
	case "stop_gateway":
		if err := value.restartScript("stop-gateway.sh"); err != nil {
			output.Error = err.Error()
		} else {
			output.OK = true
		}
	case "stop_gmsv":
		if err := value.restartScript("stop-gmsv.sh"); err != nil {
			output.Error = err.Error()
		} else {
			output.OK = true
		}
	case "stop_saac":
		if err := value.restartScript("stop-saac.sh"); err != nil {
			output.Error = err.Error()
		} else {
			output.OK = true
		}
	case "restart_gateway":
		if err := value.restartScript("restart-gateway.sh"); err != nil {
			output.Error = err.Error()
		} else {
			output.OK = true
		}
	case "notify":
		if err := value.notify(input.Message); err != nil {
			output.Error = err.Error()
		} else {
			output.OK = true
		}
	default:
		output.Error = "unsupported operator action"
	}
	writeResponse(connection, output)
}

func (value *operator) status() status {
	fallback := status{
		Gateway:  probeAddress(value.gatewayAddr),
		GMSV:     probeAddress(value.upstreamAddr),
		SAAC:     probeAddress(value.saacAddr),
		Database: probeDatabase(value.databasePath),
	}
	if scripted, ok := value.statusScript(fallback); ok {
		return scripted
	}
	return fallback
}

func (value *operator) statusScript(fallback status) (status, bool) {
	command := filepath.Join(value.packageRoot, "status-server.sh")
	info, err := os.Lstat(command)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return status{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, command).Output()
	if err != nil {
		return status{}, false
	}
	result := fallback
	found := 0
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		value = fields[0]
		switch key {
		case "gateway":
			result.Gateway = value
			found++
		case "gmsv":
			result.GMSV = value
			found++
		case "saac":
			result.SAAC = value
			found++
		}
	}
	if found == 0 {
		return status{}, false
	}
	return result, true
}

func (value *operator) restartScript(script string) error {
	return value.runScript(script, 90*time.Second)
}

func (value *operator) notify(message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return errors.New("notification is empty")
	}
	if strings.ContainsAny(message, "\r\n\x00") {
		return errors.New("notification must be a single line")
	}
	if utf8.RuneCountInString(message) > 240 {
		return errors.New("notification is limited to 240 characters")
	}
	return value.runScriptWithArgs("send-notification.sh", 10*time.Second, message)
}

func (value *operator) runScript(script string, timeout time.Duration) error {
	return value.runScriptWithArgs(script, timeout)
}

func (value *operator) runScriptWithArgs(script string, timeout time.Duration, args ...string) error {
	value.restartMu.Lock()
	defer value.restartMu.Unlock()
	command := filepath.Join(value.packageRoot, script)
	info, err := os.Lstat(command)
	if err != nil {
		return fmt.Errorf("operator command unavailable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("operator command is not a regular executable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if output, err := exec.CommandContext(ctx, command, args...).CombinedOutput(); err != nil {
		if len(output) > 2048 {
			output = output[len(output)-2048:]
		}
		return fmt.Errorf("operator command failed: %w: %s", err, output)
	}
	return nil
}

func (value *operator) restart() error {
	return value.restartScript("restart-server.sh")
}

func probeAddress(address string) string {
	if address == "" {
		return "not configured"
	}
	connection, err := net.DialTimeout("tcp", address, 800*time.Millisecond)
	if err != nil {
		return "stopped"
	}
	connection.Close()
	return "running"
}

func probeDatabase(path string) string {
	if path == "" {
		return "not configured"
	}
	info, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	if !info.Mode().IsRegular() {
		return "invalid"
	}
	return "ready"
}

func writeResponse(writer io.Writer, value response) {
	_ = json.NewEncoder(writer).Encode(value)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
