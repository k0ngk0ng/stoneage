package airuntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

func validateModelConfig(config ModelConfig) error {
	if strings.TrimSpace(config.ID) == "" {
		return fmt.Errorf("%w: model config id is required", ErrInvalidProvider)
	}
	if strings.TrimSpace(config.Name) == "" {
		return fmt.Errorf("%w: model config name is required", ErrInvalidProvider)
	}
	if config.Backend != ModelBackendCodex {
		return fmt.Errorf("%w: unsupported runtime backend %q", ErrInvalidProvider, config.Backend)
	}
	if strings.TrimSpace(config.Provider) == "" {
		return fmt.Errorf("%w: provider is required", ErrInvalidProvider)
	}
	if strings.TrimSpace(config.WireAPI) != ModelProviderResponses {
		return fmt.Errorf("%w: only the Responses wire API is supported", ErrInvalidProvider)
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		return fmt.Errorf("%w: base URL is required", ErrInvalidProvider)
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%w: base URL must be an http(s) URL", ErrInvalidProvider)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return fmt.Errorf("%w: base URL must not contain userinfo, query, or fragment", ErrInvalidProvider)
	}
	if strings.TrimSpace(config.Model) == "" {
		return fmt.Errorf("%w: model is required", ErrInvalidProvider)
	}
	effort := strings.TrimSpace(config.ReasoningEffort)
	if config.Model == "deepseek-flash" {
		switch effort {
		case ReasoningEffortLow, ReasoningEffortHigh, ReasoningEffortMax:
		default:
			return fmt.Errorf("%w: DeepSeek-Flash reasoning effort must be low, high, or max", ErrInvalidProvider)
		}
	} else {
		switch effort {
		case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
		default:
			return fmt.Errorf("%w: invalid generic reasoning effort", ErrInvalidProvider)
		}
	}
	if config.Timeout <= 0 {
		return fmt.Errorf("%w: timeout must be positive", ErrInvalidProvider)
	}
	if config.MaxOutputTokens <= 0 {
		return fmt.Errorf("%w: max output tokens must be positive", ErrInvalidProvider)
	}
	if config.DailyTokenBudget < 0 {
		return fmt.Errorf("%w: daily token budget must be non-negative", ErrInvalidProvider)
	}
	return nil
}

func (store *Store) CreateModelConfig(ctx context.Context, config ModelConfig) (ModelConfig, error) {
	return store.CreateModelConfigAs(ctx, config, "system")
}

func (store *Store) CreateModelConfigAs(ctx context.Context, config ModelConfig, actor string) (ModelConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if config.ID == "" {
		config.ID = newID("model")
	}
	if config.Provider == "" {
		config.Provider = "deepseek"
	}
	if config.Backend == "" {
		config.Backend = ModelBackendCodex
	}
	if config.WireAPI == "" {
		config.WireAPI = ModelProviderResponses
	}
	config.Name = strings.TrimSpace(config.Name)
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.Model = strings.TrimSpace(config.Model)
	config.ReasoningEffort = strings.TrimSpace(config.ReasoningEffort)
	if config.ReasoningEffort == "" && config.Model == "deepseek-flash" {
		config.ReasoningEffort = ReasoningEffortHigh
	}
	if config.Version == 0 {
		config.Version = 1
	}
	if config.CreatedAt.IsZero() {
		config.CreatedAt = store.now().UTC()
	}
	if config.UpdatedAt.IsZero() {
		config.UpdatedAt = config.CreatedAt
	}
	if err := validateModelConfig(config); err != nil {
		return ModelConfig{}, err
	}
	actor = cleanActor(actor)
	if actor == "" {
		actor = "system"
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return ModelConfig{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO ai_model_configs
		(id, name, backend, provider, base_url, model, wire_api, reasoning_effort, timeout_ns, max_output_tokens,
		 daily_token_budget, has_key, version, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, config.ID, config.Name,
		config.Backend, config.Provider, strings.TrimRight(config.BaseURL, "/"), config.Model, config.WireAPI, config.ReasoningEffort,
		config.Timeout.Nanoseconds(), config.MaxOutputTokens, config.DailyTokenBudget,
		boolInt(config.HasKey), config.Version, store.timestamp(config.CreatedAt), store.timestamp(config.UpdatedAt))
	if err != nil {
		return ModelConfig{}, fmt.Errorf("create AI model config: %w", err)
	}
	detail := jsonObject(map[string]any{"version": config.Version, "has_key": config.HasKey})
	if _, err := store.recordEventTx(ctx, tx, modelConfigEventID(config.ID), "model_config.created", actor, detail); err != nil {
		return ModelConfig{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelConfig{}, fmt.Errorf("commit AI model config: %w", err)
	}
	return cloneModelConfig(config), nil
}

func (store *Store) GetModelConfig(ctx context.Context, id string) (ModelConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	row := store.db.QueryRowContext(ctx, `SELECT id, name, backend, provider, base_url, model, wire_api, reasoning_effort,
        timeout_ns, max_output_tokens, daily_token_budget, has_key, version,
        created_at, updated_at FROM ai_model_configs WHERE id=?`, id)
	config, err := scanModelConfig(row)
	if err != nil {
		return ModelConfig{}, err
	}
	return store.refreshModelConfigKey(config), nil
}

func (store *Store) ListModelConfigs(ctx context.Context) ([]ModelConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id, name, backend, provider, base_url, model, wire_api, reasoning_effort,
        timeout_ns, max_output_tokens, daily_token_budget, has_key, version,
        created_at, updated_at FROM ai_model_configs ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list AI model configs: %w", err)
	}
	defer rows.Close()
	configs := make([]ModelConfig, 0)
	for rows.Next() {
		config, err := scanModelConfig(rows)
		if err != nil {
			return nil, err
		}
		configs = append(configs, store.refreshModelConfigKey(config))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return configs, nil
}

func (store *Store) UpdateModelConfigCAS(ctx context.Context, id string, expectedVersion int64, patch ModelConfigPatch) (ModelConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if expectedVersion < 1 {
		return ModelConfig{}, ErrConflict
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return ModelConfig{}, err
	}
	defer tx.Rollback()
	config, err := scanModelConfigTx(ctx, tx, id)
	if err != nil {
		return ModelConfig{}, err
	}
	if config.Version != expectedVersion {
		return ModelConfig{}, ErrConflict
	}
	changed := make([]string, 0, 8)
	if patch.Name != nil {
		config.Name = strings.TrimSpace(*patch.Name)
		changed = append(changed, "name")
	}
	if patch.Backend != nil {
		config.Backend = strings.TrimSpace(*patch.Backend)
		changed = append(changed, "backend")
	}
	if patch.Provider != nil {
		config.Provider = strings.TrimSpace(*patch.Provider)
		changed = append(changed, "provider")
	}
	if patch.BaseURL != nil {
		config.BaseURL = strings.TrimRight(strings.TrimSpace(*patch.BaseURL), "/")
		changed = append(changed, "base_url")
	}
	if patch.Model != nil {
		config.Model = strings.TrimSpace(*patch.Model)
		changed = append(changed, "model")
	}
	if patch.WireAPI != nil {
		config.WireAPI = strings.TrimSpace(*patch.WireAPI)
		changed = append(changed, "wire_api")
	}
	if patch.ReasoningEffort != nil {
		config.ReasoningEffort = strings.TrimSpace(*patch.ReasoningEffort)
		changed = append(changed, "reasoning_effort")
	}
	if patch.Timeout != nil {
		config.Timeout = *patch.Timeout
		changed = append(changed, "timeout")
	}
	if patch.MaxOutputTokens != nil {
		config.MaxOutputTokens = *patch.MaxOutputTokens
		changed = append(changed, "max_output_tokens")
	}
	if patch.DailyTokenBudget != nil {
		config.DailyTokenBudget = *patch.DailyTokenBudget
		changed = append(changed, "daily_token_budget")
	}
	if patch.HasKey != nil {
		config.HasKey = *patch.HasKey
		changed = append(changed, "has_key")
	}
	if len(changed) == 0 {
		return store.refreshModelConfigKey(config), nil
	}
	if err := validateModelConfig(config); err != nil {
		return ModelConfig{}, err
	}
	config.Version++
	config.UpdatedAt = store.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE ai_model_configs SET name=?, backend=?, provider=?, base_url=?, model=?, wire_api=?, reasoning_effort=?,
        timeout_ns=?, max_output_tokens=?, daily_token_budget=?, has_key=?, version=?, updated_at=?
		WHERE id=? AND version=?`, config.Name, config.Backend, config.Provider, strings.TrimRight(config.BaseURL, "/"), config.Model, config.WireAPI, config.ReasoningEffort,
		config.Timeout.Nanoseconds(), config.MaxOutputTokens, config.DailyTokenBudget, boolInt(config.HasKey),
		config.Version, store.timestamp(config.UpdatedAt), id, expectedVersion)
	if err != nil {
		return ModelConfig{}, fmt.Errorf("update AI model config: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ModelConfig{}, ErrConflict
	}
	detail := jsonObject(map[string]any{"version": config.Version, "changed": changed})
	actor := cleanActor(patch.Actor)
	if actor == "" {
		actor = "system"
	}
	if _, err := store.recordEventTx(ctx, tx, modelConfigEventID(id), "model_config.updated", actor, detail); err != nil {
		return ModelConfig{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelConfig{}, fmt.Errorf("commit AI model config update: %w", err)
	}
	return cloneModelConfig(config), nil
}

func (store *Store) DeleteModelConfigCAS(ctx context.Context, id string, expectedVersion int64, actor string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	config, err := scanModelConfigTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if config.Version != expectedVersion {
		return ErrConflict
	}
	detail := jsonObject(map[string]any{"version": expectedVersion})
	if _, err := store.recordEventTx(ctx, tx, modelConfigEventID(id), "model_config.deleted", cleanActor(actor), detail); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM ai_model_configs WHERE id=? AND version=?", id, expectedVersion)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (store *Store) SetModelConfigKeyPresence(ctx context.Context, id string, expectedVersion int64, hasKey bool, actor string) (ModelConfig, error) {
	return store.UpdateModelConfigCAS(ctx, id, expectedVersion, ModelConfigPatch{HasKey: &hasKey, Actor: actor})
}

// GetDefaultModelConfigID returns the server-wide model configuration
// selected by an administrator.  An empty value with a nil error means that
// no default has been selected yet.
func (store *Store) GetDefaultModelConfigID(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var value string
	err := store.db.QueryRowContext(ctx, "SELECT value FROM ai_runtime_settings WHERE key=?", "default_model_config_id").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get default AI model config: %w", err)
	}
	return value, nil
}

// SetDefaultModelConfigID validates that the selected config exists, then
// atomically records the selection and an audit event.  The browser cannot
// provide a secret path or executable path through this method.
func (store *Store) SetDefaultModelConfigID(ctx context.Context, id, actor string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	id = strings.TrimSpace(id)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if id != "" {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM ai_model_configs WHERE id=?", id).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
	}
	now := store.timestamp(store.now())
	if id == "" {
		if _, err := tx.ExecContext(ctx, "DELETE FROM ai_runtime_settings WHERE key=?", "default_model_config_id"); err != nil {
			return fmt.Errorf("clear default AI model config: %w", err)
		}
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO ai_runtime_settings(key, value, updated_at)
		VALUES(?, ?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		"default_model_config_id", id, now); err != nil {
		return fmt.Errorf("set default AI model config: %w", err)
	}
	detail := jsonObject(map[string]any{"model_config_id": id})
	if _, err := store.recordEventTx(ctx, tx, "runtime", "model_config.default_changed", cleanActor(actor), detail); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit default AI model config: %w", err)
	}
	return nil
}

func (store *Store) refreshModelConfigKey(config ModelConfig) ModelConfig {
	if store.secretStore == nil {
		return config
	}
	hasKey, err := store.secretStore.HasKey(config.ID)
	if err == nil {
		config.HasKey = hasKey
	} else {
		// Never advertise a key when the fixed-path file is missing, unsafe, or
		// otherwise unreadable.
		config.HasKey = false
	}
	return config
}

type modelConfigScanner interface{ Scan(dest ...any) error }

func scanModelConfig(row modelConfigScanner) (ModelConfig, error) {
	var config ModelConfig
	var timeoutNS int64
	var hasKey int
	var created, updated string
	if err := row.Scan(&config.ID, &config.Name, &config.Backend, &config.Provider, &config.BaseURL, &config.Model, &config.WireAPI, &config.ReasoningEffort,
		&timeoutNS, &config.MaxOutputTokens, &config.DailyTokenBudget, &hasKey, &config.Version,
		&created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ModelConfig{}, ErrNotFound
		}
		return ModelConfig{}, err
	}
	config.Timeout = time.Duration(timeoutNS)
	if strings.TrimSpace(config.WireAPI) == "" {
		// Databases created before WireAPI was introduced (or rows written by
		// an older migration) use the Responses protocol by default.
		config.WireAPI = ModelProviderResponses
	}
	config.HasKey = hasKey == 1
	var err error
	config.CreatedAt, err = parseTimestamp(created)
	if err != nil {
		return ModelConfig{}, err
	}
	config.UpdatedAt, err = parseTimestamp(updated)
	if err != nil {
		return ModelConfig{}, err
	}
	return config, nil
}

func scanModelConfigTx(ctx context.Context, tx *sql.Tx, id string) (ModelConfig, error) {
	return scanModelConfig(tx.QueryRowContext(ctx, `SELECT id, name, backend, provider, base_url, model, wire_api, reasoning_effort,
        timeout_ns, max_output_tokens, daily_token_budget, has_key, version,
        created_at, updated_at FROM ai_model_configs WHERE id=?`, id))
}

func cloneModelConfig(config ModelConfig) ModelConfig { return config }

func modelConfigEventID(id string) string { return "model_config:" + id }

func jsonObject(value map[string]any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
