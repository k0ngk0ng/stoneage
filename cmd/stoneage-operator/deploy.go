package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Docker tags do not accept the '+' used by SemVer build metadata. Keep the
// web/operator format to the Docker-safe vMAJOR.MINOR.PATCH[-prerelease]
// subset so the value is safe in both image references and container names.
var releaseVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
var imageRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

func (value *operator) deploymentStatus() deploymentStatus {
	result := deploymentStatus{Phase: "idle"}
	data, err := os.ReadFile(value.statePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			result.Phase = "unknown"
			result.Message = err.Error()
		}
		return result
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, item, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "version":
			result.Version = item
		case "phase":
			result.Phase = item
		case "message":
			result.Message = item
		case "updated_at":
			result.UpdatedAt = item
		case "backup":
			result.Backup = item
		}
	}
	return result
}

func (value *operator) deployVersion(version string) error {
	if !releaseVersionPattern.MatchString(version) {
		return errors.New("版本号必须是 v1.2.3 格式")
	}
	if value.projectHostRoot == "" || !filepath.IsAbs(value.projectHostRoot) {
		return errors.New("未配置生产项目绝对路径，网页下发版已禁用")
	}
	if !filepath.IsAbs(value.projectRoot) || value.composeFile == "" {
		return errors.New("发布容器路径配置无效")
	}
	if _, err := os.Stat(value.projectHostRoot); err != nil {
		return fmt.Errorf("生产项目路径不可用: %w", err)
	}
	if _, err := os.Stat(value.composeFile); err != nil {
		return fmt.Errorf("Compose 文件不可用: %w", err)
	}
	for label, image := range map[string]string{"control": value.controlImage, "legacy": value.legacyImage} {
		if !imageRepositoryPattern.MatchString(image) {
			return fmt.Errorf("%s 镜像仓库配置无效", label)
		}
	}
	for label, path := range map[string]string{"GMSV": value.gmsvDataRoot, "SAAC": value.saacDataRoot} {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("未配置 %s 数据绝对路径，无法备份", label)
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s 数据目录不可用: %w", label, err)
		}
	}

	value.restartMu.Lock()
	defer value.restartMu.Unlock()
	current := value.deploymentStatus()
	if isDeploymentRunning(current.Phase) {
		return fmt.Errorf("已有版本发布任务正在进行（%s）", current.Version)
	}

	name := "stoneage-deploy-" + strings.TrimPrefix(version, "v")
	name = strings.NewReplacer(".", "-", "+", "-", "/", "-").Replace(name)
	args := []string{
		"run", "--detach", "--rm", "--name", name,
		"--volume", "/var/run/docker.sock:/var/run/docker.sock",
		"--volume", value.projectHostRoot + ":" + value.projectRoot + ":ro",
		"--volume", value.stateVolume + ":/state",
		"--volume", value.gmsvDataRoot + ":" + value.gmsvDataRoot + ":ro",
		"--volume", value.saacDataRoot + ":" + value.saacDataRoot + ":ro",
		// Compose is evaluated inside this detached container, but bind mount
		// sources are resolved by the Docker daemon on the host. Keep both
		// paths explicit: the container path locates the checked-out Compose
		// file, while the host path is used for volume interpolation.
		"--env", "STONEAGE_PROJECT_CONTAINER_ROOT=" + value.projectRoot,
		"--env", "STONEAGE_PROJECT_ROOT=" + value.projectHostRoot,
		"--env", "STONEAGE_PROJECT_HOST_ROOT=" + value.projectHostRoot,
		"--env", "STONEAGE_COMPOSE_FILE=" + value.composeFile,
		"--env", "STONEAGE_CONTROL_IMAGE=" + value.controlImage,
		"--env", "STONEAGE_LEGACY_IMAGE=" + value.legacyImage,
		"--env", "STONEAGE_OPERATOR_STATE_VOLUME=" + value.stateVolume,
		"--env", "STONEAGE_AUTH_VOLUME=" + value.authVolume,
		"--env", "STONEAGE_GMSV_DATA_ROOT=" + value.gmsvDataRoot,
		"--env", "STONEAGE_SAAC_DATA_ROOT=" + value.saacDataRoot,
		"--env", "STONEAGE_DEPLOY_STATE=/state/deploy.status",
		value.controlImage + ":" + version,
		"/opt/stoneage/compose/deploy-version.sh", version,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, value.dockerBin, args...).CombinedOutput()
	if err != nil {
		if len(output) > 2048 {
			output = output[len(output)-2048:]
		}
		return fmt.Errorf("启动版本发布任务失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func isDeploymentRunning(phase string) bool {
	switch phase {
	case "preparing", "backup", "pulling", "stopping", "updating", "verifying", "rolling_back":
		return true
	default:
		return false
	}
}
