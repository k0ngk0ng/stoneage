package admin

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

func playerJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func playerJSONError(response http.ResponseWriter, status int, message string) {
	playerJSON(response, status, map[string]string{"error": message})
}

func playerManagerError(response http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, playerdata.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, playerdata.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, playerdata.ErrUnavailable):
		status = http.StatusServiceUnavailable
	}
	playerJSONError(response, status, err.Error())
}

func (server *Server) playerAPI(response http.ResponseWriter, request *http.Request, data *pageData) {
	if request.URL.Path == "/api/player-catalog" {
		server.playerCatalogAPI(response, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/api/player-assets/") {
		if request.Method != http.MethodGet {
			playerJSONError(response, 405, "请求方式不支持")
			return
		}
		if server.playerAssets == nil {
			playerJSONError(response, 503, "外观资源尚未配置")
			return
		}
		server.playerAssets.ServeHTTP(response, request)
		return
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/api/accounts/"), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[1] != "players" {
		playerJSONError(response, 404, "页面不存在")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		playerJSONError(response, 404, "账号不存在")
		return
	}
	account, err := server.store.GetAccount(request.Context(), id)
	if err != nil {
		playerJSONError(response, 404, "账号不存在")
		return
	}
	if server.players == nil {
		playerJSONError(response, 503, "玩家管理服务尚未配置")
		return
	}
	if len(parts) == 2 {
		if request.Method != http.MethodGet {
			playerJSONError(response, 405, "请求方式不支持")
			return
		}
		characters, err := server.players.List(request.Context(), account.Username)
		if err != nil {
			playerManagerError(response, err)
			return
		}
		if characters == nil {
			characters = []playerdata.Character{}
		}
		playerJSON(response, 200, map[string]any{"characters": characters})
		return
	}
	slot, err := strconv.Atoi(parts[2])
	if err != nil || slot < 0 || slot > 1 {
		playerJSONError(response, 404, "角色不存在")
		return
	}
	if request.Method == http.MethodGet {
		snapshot, err := server.players.Get(request.Context(), account.Username, slot)
		if err != nil {
			playerManagerError(response, err)
			return
		}
		playerJSON(response, 200, snapshot)
		return
	}
	if request.Method != http.MethodPost {
		playerJSONError(response, 405, "请求方式不支持")
		return
	}
	provided := request.Header.Get("X-CSRF-Token")
	if provided == "" || !hmac.Equal([]byte(provided), []byte(data.CSRF)) {
		playerJSONError(response, 403, "请求校验失败，请刷新页面")
		return
	}
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		playerJSONError(response, 415, "请使用 JSON 请求")
		return
	}
	var mutation playerdata.Mutation
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&mutation); err != nil {
		playerJSONError(response, 400, "修改参数无效")
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		playerJSONError(response, 400, "修改参数无效")
		return
	}
	if revision, err := hex.DecodeString(mutation.Revision); err != nil || len(revision) != 32 {
		playerJSONError(response, 400, "缺少有效的角色版本，请刷新页面")
		return
	}
	switch mutation.Action {
	case "set_character", "grant_item", "set_item", "delete_item", "grant_pet", "set_pet", "delete_pet", "set_pet_skill", "delete_pet_skill":
	default:
		playerJSONError(response, 400, "修改操作不支持")
		return
	}
	// Persist intent before touching game state. Even if the process dies after
	// the game acknowledges an operation, its target and expected revision remain
	// traceable. A failed audit write prevents the mutation from running.
	detailBytes, _ := json.Marshal(struct {
		CharacterSlot int                 `json:"character_slot"`
		Mutation      playerdata.Mutation `json:"mutation"`
	}{slot, mutation})
	if err = server.store.RecordAudit(request.Context(), adminID(data), "player_change_requested", account.Username, server.requestSourceIP(request), string(detailBytes)); err != nil {
		playerJSONError(response, 503, "无法记录审计日志，未执行修改")
		return
	}
	snapshot, err := server.players.Apply(request.Context(), account.Username, slot, mutation)
	event := "player_changed"
	result := map[string]any{"character_slot": slot, "mutation": mutation, "revision": snapshot.Revision}
	if err != nil {
		event = "player_change_failed"
		result["error"] = err.Error()
	}
	resultBytes, _ := json.Marshal(result)
	auditContext, cancelAudit := context.WithTimeout(context.WithoutCancel(request.Context()), 3*time.Second)
	defer cancelAudit()
	if auditErr := server.store.RecordAudit(auditContext, adminID(data), event, account.Username, server.requestSourceIP(request), string(resultBytes)); auditErr != nil {
		log.Printf("player management audit result failed account_id=%d slot=%d: %v", id, slot, auditErr)
	}
	if err != nil {
		playerManagerError(response, err)
		return
	}
	playerJSON(response, 200, snapshot)
}

func (server *Server) loadPlayerCatalog() (*gamecatalog.Catalog, error) {
	server.playerCatalogMu.Lock()
	defer server.playerCatalogMu.Unlock()
	if server.playerCatalog != nil {
		return server.playerCatalog, nil
	}
	if server.playerCatalogLoader == nil {
		return nil, errors.New("game catalog loader is not configured")
	}
	catalog, err := server.playerCatalogLoader()
	if err != nil {
		return nil, err
	}
	if catalog == nil {
		return nil, errors.New("game catalog loader returned nil catalog")
	}
	server.playerCatalog = catalog
	return catalog, nil
}

func (server *Server) playerCatalogAPI(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		playerJSONError(response, 405, "请求方式不支持")
		return
	}
	catalog, err := server.loadPlayerCatalog()
	if err != nil {
		playerJSONError(response, 503, "游戏目录尚未加载")
		return
	}
	kind := gamecatalog.Kind(request.URL.Query().Get("kind"))
	if kind != gamecatalog.KindItem && kind != gamecatalog.KindPet && kind != gamecatalog.KindPetSkill {
		playerJSONError(response, 400, "请选择物品、宠物或宠物技能")
		return
	}
	query := request.URL.Query().Get("q")
	if len(query) > 200 {
		playerJSONError(response, 400, "搜索内容过长")
		return
	}
	limit, offset := 40, 0
	for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		if raw := request.URL.Query().Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 {
				playerJSONError(response, 400, "分页参数无效")
				return
			}
			*target = value
		}
	}
	if limit < 1 || limit > 100 {
		playerJSONError(response, 400, "每页数量须为 1–100")
		return
	}
	entries := make([]gamecatalog.Entry, 0)
	for _, entry := range catalog.Search(query) {
		if entry.Kind == kind {
			entries = append(entries, entry)
		}
	}
	total := len(entries)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	playerJSON(response, 200, map[string]any{"entries": entries[offset:end], "total": total})
}
