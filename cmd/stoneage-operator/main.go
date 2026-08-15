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

	"golang.org/x/text/encoding/simplifiedchinese"
)

type request struct {
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
	Version string `json:"version,omitempty"`
}

type status struct {
	Gateway  string `json:"gateway"`
	GMSV     string `json:"gmsv"`
	SAAC     string `json:"saac"`
	Database string `json:"database"`
}

type response struct {
	OK         bool             `json:"ok"`
	Error      string           `json:"error,omitempty"`
	Status     status           `json:"status,omitempty"`
	Deployment deploymentStatus `json:"deployment,omitempty"`
}

type deploymentStatus struct {
	Version   string `json:"version"`
	Phase     string `json:"phase"`
	Message   string `json:"message,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Backup    string `json:"backup,omitempty"`
}

type operator struct {
	packageRoot     string
	socket          string
	gatewayAddr     string
	upstreamAddr    string
	saacAddr        string
	databasePath    string
	projectRoot     string
	projectHostRoot string
	composeFile     string
	controlImage    string
	legacyImage     string
	dockerBin       string
	statePath       string
	stateVolume     string
	authVolume      string
	gmsvDataRoot    string
	saacDataRoot    string
	restartMu       sync.Mutex
}

func main() {
	var value operator
	flag.StringVar(&value.socket, "socket", envOr("STONEAGE_OPERATOR_SOCKET", "/run/stoneage/operator.sock"), "Unix socket path")
	flag.StringVar(&value.packageRoot, "package-root", envOr("STONEAGE_PACKAGE_ROOT", "."), "Linux release package root")
	flag.StringVar(&value.gatewayAddr, "gateway", envOr("STONEAGE_GATEWAY_ADDRESS", "127.0.0.1:9065"), "gateway status address")
	flag.StringVar(&value.upstreamAddr, "upstream", envOr("STONEAGE_UPSTREAM_ADDRESS", "127.0.0.1:19065"), "GMSV status address")
	flag.StringVar(&value.saacAddr, "saac", envOr("STONEAGE_SAAC_ADDRESS", "127.0.0.1:9300"), "SAAC status address")
	flag.StringVar(&value.databasePath, "auth-db", envOr("STONEAGE_AUTH_DB", ""), "SQLite auth database path")
	flag.StringVar(&value.projectRoot, "project-root", envOr("STONEAGE_PROJECT_ROOT", "/host-project"), "project path visible inside deployment container")
	flag.StringVar(&value.projectHostRoot, "project-host-root", os.Getenv("STONEAGE_PROJECT_HOST_ROOT"), "absolute project path on the Docker host")
	flag.StringVar(&value.composeFile, "compose-file", envOr("STONEAGE_COMPOSE_FILE", "/host-project/docker-compose.yml"), "fixed compose file path")
	flag.StringVar(&value.controlImage, "control-image", envOr("STONEAGE_CONTROL_IMAGE", "stoneage-control-plane"), "control-plane image repository")
	flag.StringVar(&value.legacyImage, "legacy-image", envOr("STONEAGE_LEGACY_IMAGE", "stoneage-legacy-runtime"), "legacy runtime image repository")
	flag.StringVar(&value.dockerBin, "docker", envOr("STONEAGE_DOCKER_BIN", "docker"), "Docker CLI path")
	flag.StringVar(&value.statePath, "state", envOr("STONEAGE_DEPLOY_STATE", "/state/deploy.status"), "deployment state file")
	flag.StringVar(&value.stateVolume, "state-volume", envOr("STONEAGE_OPERATOR_STATE_VOLUME", "stoneage-operator-state"), "deployment state Docker volume")
	flag.StringVar(&value.authVolume, "auth-volume", envOr("STONEAGE_AUTH_VOLUME", "stoneage-auth"), "auth database Docker volume")
	flag.StringVar(&value.gmsvDataRoot, "gmsv-data-root", os.Getenv("STONEAGE_GMSV_DATA_ROOT"), "absolute GMSV data path on Docker host")
	flag.StringVar(&value.saacDataRoot, "saac-data-root", os.Getenv("STONEAGE_SAAC_DATA_ROOT"), "absolute SAAC data path on Docker host")
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
	case "deploy_version":
		if err := value.deployVersion(input.Version); err != nil {
			output.Error = err.Error()
		} else {
			output.OK = true
			output.Deployment = value.deploymentStatus()
		}
	case "deploy_status":
		output.Deployment = value.deploymentStatus()
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
	encoded, err := notificationCP936(message)
	if err != nil {
		return err
	}
	return value.runScriptWithArgs("send-notification.sh", 10*time.Second, string(encoded))
}

func notificationCP936(message string) ([]byte, error) {
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(message))
	if err != nil {
		return nil, fmt.Errorf("notification contains characters not representable in CP936: %w", err)
	}
	// GMSV reads the queue through a 1024-byte line buffer. Keep the encoded
	// message within one line even when a CP936 character uses two bytes.
	if len(encoded) > 1023 {
		return nil, errors.New("notification is too large for the CP936 queue")
	}
	return encoded, nil
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
