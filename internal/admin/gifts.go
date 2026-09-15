package admin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
	"github.com/k0ngk0ng/stoneage/internal/playerbridge"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

const (
	giftPreviewLifetime = 10 * time.Minute
	maxGiftPreviews     = 32
	maxGiftItemCount    = 15
	maxGiftPetCount     = 5
)

// giftDefinition stores catalog template IDs and quantities. Display names
// are resolved from the catalog; client-supplied names do not select templates.
type giftDefinition struct {
	Items []giftDefinitionEntry `json:"items"`
	Pets  []giftDefinitionEntry `json:"pets"`
}

type giftDefinitionEntry struct {
	ID       int `json:"id"`
	Quantity int `json:"quantity"`
}

// Accept either catalog identifier spelling and persist the canonical id form.
func (entry *giftDefinitionEntry) UnmarshalJSON(data []byte) error {
	var value struct {
		ID         *int `json:"id"`
		TemplateID *int `json:"template_id"`
		Quantity   int  `json:"quantity"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if value.ID == nil && value.TemplateID == nil {
		entry.ID = -1
	} else if value.ID != nil && value.TemplateID != nil && *value.ID != *value.TemplateID {
		return errors.New("礼包目录编号不一致")
	} else if value.TemplateID != nil {
		entry.ID = *value.TemplateID
	} else {
		entry.ID = *value.ID
	}
	entry.Quantity = value.Quantity
	return nil
}

type giftPackageRequest struct {
	Name       string         `json:"name"`
	Definition giftDefinition `json:"definition"`
}

type giftRunRequest struct {
	PackageID       int64  `json:"package_id"`
	Scope           string `json:"scope"`
	AccountID       *int64 `json:"account_id,omitempty"`
	AccountUsername string `json:"account_username,omitempty"`
	CharacterSlot   *int   `json:"character_slot,omitempty"`
	PreviewToken    string `json:"preview_token,omitempty"`
}

// giftPreviewBinding is what a confirmation is allowed to repeat. The
// resolved account ID is used for a single target, so an account-name alias
// cannot point a preview at a different account between preview and confirm.
type giftPreviewBinding struct {
	PackageID     int64
	Scope         string
	AccountID     int64
	CharacterSlot int
}

// giftPreview is deliberately process-local. A preview is a short-lived
// safety confirmation, while the run and every delivery are durable records.
// A process restart therefore invalidates all outstanding previews.
type giftPreview struct {
	Token           string
	AdminID         int64
	PackageID       int64
	Scope           string
	PackageSnapshot json.RawMessage
	Targets         []auth.GiftTarget
	Binding         giftPreviewBinding
	CreatedAt       time.Time
	ExpiresAt       time.Time
	RunID           int64
}

type giftPreviewSkipped struct {
	AccountID     int64  `json:"account_id"`
	Account       string `json:"account"`
	CharacterSlot int    `json:"character_slot"`
	CharacterName string `json:"character"`
	Reason        string `json:"reason"`
}

type giftPreviewResponse struct {
	Targets      int                  `json:"targets"`
	Eligible     int                  `json:"eligible"`
	Skipped      []giftPreviewSkipped `json:"skipped"`
	PreviewToken string               `json:"preview_token,omitempty"`
}

type giftPackageSnapshot struct {
	Name       string          `json:"name"`
	Definition json.RawMessage `json:"definition"`
}

type giftRunView struct {
	auth.GiftRun
	Scope string `json:"scope"`
}

func makeGiftRunView(run auth.GiftRun) giftRunView {
	return giftRunView{GiftRun: run, Scope: run.TargetScope}
}

func makeGiftPackageSnapshot(packageValue auth.GiftPackage) (json.RawMessage, error) {
	return json.Marshal(giftPackageSnapshot{
		Name:       packageValue.Name,
		Definition: append(json.RawMessage(nil), packageValue.Definition...),
	})
}

func (server *Server) giftClockNow() time.Time {
	if server.giftNowFunc != nil {
		return server.giftNowFunc()
	}
	return time.Now()
}

// decodeGiftJSON applies the same content-type and trailing-data checks to
// every mutation endpoint. Handler already wraps the body in a 64 KiB
// MaxBytesReader; the explicit error mapping keeps oversized JSON from being
// mistaken for a malformed package.
func decodeGiftJSON(request *http.Request, value any) error {
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return errGiftJSONContentType
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return errGiftJSONTrailing
		}
		return err
	}
	return nil
}

var (
	errGiftJSONContentType = errors.New("请使用 JSON 请求")
	errGiftJSONTrailing    = errors.New("请求参数无效")
	errGiftPreviewExpired  = errors.New("预览令牌无效或已过期，请重新预览")
	errGiftPreviewOwner    = errors.New("预览令牌不属于当前管理员")
)

func giftJSONDecodeError(response http.ResponseWriter, err error) {
	if errors.Is(err, errGiftJSONContentType) {
		playerJSONError(response, http.StatusUnsupportedMediaType, err.Error())
		return
	}
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		playerJSONError(response, http.StatusRequestEntityTooLarge, "请求内容过大")
		return
	}
	playerJSONError(response, http.StatusBadRequest, "请求参数无效")
}

func (server *Server) giftMutationAllowed(request *http.Request, data *pageData) bool {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}
	provided := request.Header.Get("X-CSRF-Token")
	return provided != "" && data != nil && hmac.Equal([]byte(provided), []byte(data.CSRF))
}

func (server *Server) giftAPI(response http.ResponseWriter, request *http.Request, data *pageData) {
	path := strings.TrimSuffix(request.URL.Path, "/")
	switch {
	case path == "/api/gift-packages":
		server.giftPackagesAPI(response, request, data)
	case strings.HasPrefix(path, "/api/gift-packages/"):
		server.giftPackageAPI(response, request, data)
	case path == "/api/gift-preview":
		server.giftPreviewAPI(response, request, data)
	case path == "/api/gift-runs":
		server.giftRunsAPI(response, request, data)
	case strings.HasPrefix(path, "/api/gift-runs/"):
		server.giftRunAPI(response, request, data)
	case path == "/api/gift-characters":
		server.giftCharactersAPI(response, request)
	default:
		playerJSONError(response, http.StatusNotFound, "页面不存在")
	}
}

func (server *Server) giftPackagesAPI(response http.ResponseWriter, request *http.Request, data *pageData) {
	switch request.Method {
	case http.MethodGet:
		packages, err := server.store.ListGiftPackages(request.Context())
		if err != nil {
			playerJSONError(response, http.StatusInternalServerError, "礼包读取失败")
			return
		}
		if packages == nil {
			packages = []auth.GiftPackage{}
		}
		playerJSON(response, http.StatusOK, map[string]any{"packages": packages})
		return
	case http.MethodPost:
		if !server.giftMutationAllowed(request, data) {
			playerJSONError(response, http.StatusForbidden, "请求校验失败，请刷新页面")
			return
		}
		var payload giftPackageRequest
		if err := decodeGiftJSON(request, &payload); err != nil {
			giftJSONDecodeError(response, err)
			return
		}
		definition, err := server.normalizeGiftDefinition(request.Context(), payload.Definition)
		if err != nil {
			server.giftDefinitionError(response, err)
			return
		}
		server.giftMu.Lock()
		packageValue, err := server.store.CreateGiftPackage(request.Context(), payload.Name, definition)
		server.giftMu.Unlock()
		if err != nil {
			server.giftStoreError(response, err)
			return
		}
		_ = server.store.RecordAudit(request.Context(), adminID(data), "gift_package_created", "", server.requestSourceIP(request), strconv.FormatInt(packageValue.ID, 10))
		playerJSON(response, http.StatusCreated, map[string]any{"package": packageValue})
		return
	default:
		playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
	}
}

func parseGiftPathID(path, prefix string) (int64, bool) {
	value := strings.TrimPrefix(path, prefix)
	if value == "" || strings.Contains(value, "/") {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0
}

func (server *Server) giftPackageAPI(response http.ResponseWriter, request *http.Request, data *pageData) {
	id, ok := parseGiftPathID(strings.TrimSuffix(request.URL.Path, "/"), "/api/gift-packages/")
	if !ok {
		playerJSONError(response, http.StatusNotFound, "礼包不存在")
		return
	}
	switch request.Method {
	case http.MethodGet:
		packageValue, err := server.store.GetGiftPackage(request.Context(), id)
		if err != nil {
			server.giftStoreError(response, err)
			return
		}
		playerJSON(response, http.StatusOK, map[string]any{"package": packageValue})
	case http.MethodPut:
		if !server.giftMutationAllowed(request, data) {
			playerJSONError(response, http.StatusForbidden, "请求校验失败，请刷新页面")
			return
		}
		var payload giftPackageRequest
		if err := decodeGiftJSON(request, &payload); err != nil {
			giftJSONDecodeError(response, err)
			return
		}
		definition, err := server.normalizeGiftDefinition(request.Context(), payload.Definition)
		if err != nil {
			server.giftDefinitionError(response, err)
			return
		}
		server.giftMu.Lock()
		packageValue, err := server.store.UpdateGiftPackage(request.Context(), id, payload.Name, definition)
		server.giftMu.Unlock()
		if err != nil {
			server.giftStoreError(response, err)
			return
		}
		_ = server.store.RecordAudit(request.Context(), adminID(data), "gift_package_updated", "", server.requestSourceIP(request), strconv.FormatInt(id, 10))
		playerJSON(response, http.StatusOK, map[string]any{"package": packageValue})
	case http.MethodDelete:
		if !server.giftMutationAllowed(request, data) {
			playerJSONError(response, http.StatusForbidden, "请求校验失败，请刷新页面")
			return
		}
		server.giftMu.Lock()
		err := server.store.DeleteGiftPackage(request.Context(), id)
		server.giftMu.Unlock()
		if err != nil {
			server.giftStoreError(response, err)
			return
		}
		_ = server.store.RecordAudit(request.Context(), adminID(data), "gift_package_deleted", "", server.requestSourceIP(request), strconv.FormatInt(id, 10))
		playerJSON(response, http.StatusOK, map[string]any{"ok": true})
	default:
		playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
	}
}

func (server *Server) giftStoreError(response http.ResponseWriter, err error) {
	if errors.Is(err, auth.ErrNotFound) {
		playerJSONError(response, http.StatusNotFound, "礼包不存在")
		return
	}
	playerJSONError(response, http.StatusBadRequest, err.Error())
}

func (server *Server) giftDefinitionError(response http.ResponseWriter, err error) {
	if errors.Is(err, playerdata.ErrUnavailable) {
		playerJSONError(response, http.StatusServiceUnavailable, err.Error())
		return
	}
	playerJSONError(response, http.StatusBadRequest, err.Error())
}

func (server *Server) normalizeGiftDefinition(ctx context.Context, input giftDefinition) (json.RawMessage, error) {
	catalog, err := server.loadPlayerCatalog()
	if err != nil {
		return nil, fmt.Errorf("%w: 游戏目录尚未加载", playerdata.ErrUnavailable)
	}
	items, itemCount, err := normalizeGiftEntries(input.Items, gamecatalog.KindItem, catalog, maxGiftItemCount)
	if err != nil {
		return nil, err
	}
	pets, petCount, err := normalizeGiftEntries(input.Pets, gamecatalog.KindPet, catalog, maxGiftPetCount)
	if err != nil {
		return nil, err
	}
	if itemCount == 0 && petCount == 0 {
		return nil, errors.New("礼包至少需要一个物品或宠物")
	}
	definition := giftDefinition{
		Items: items,
		Pets:  pets,
	}
	return json.Marshal(definition)
}

func normalizeGiftEntries(input []giftDefinitionEntry, kind gamecatalog.Kind, catalog *gamecatalog.Catalog, maximum int) ([]giftDefinitionEntry, int, error) {
	entries := make([]giftDefinitionEntry, 0, len(input))
	positions := make(map[int]int, len(input))
	total := 0
	for _, entry := range input {
		if entry.ID < 0 {
			return nil, 0, errors.New("礼包目录编号无效")
		}
		if entry.Quantity <= 0 {
			return nil, 0, errors.New("礼包数量必须为正整数")
		}
		if _, ok := catalog.Find(kind, entry.ID); !ok {
			return nil, 0, fmt.Errorf("礼包目录中不存在 %s %d", kind, entry.ID)
		}
		if entry.Quantity > maximum-total {
			return nil, 0, fmt.Errorf("礼包中的%s数量最多为%d", kindLabel(kind), maximum)
		}
		total += entry.Quantity
		if index, ok := positions[entry.ID]; ok {
			entries[index].Quantity += entry.Quantity
			continue
		}
		positions[entry.ID] = len(entries)
		entries = append(entries, giftDefinitionEntry{ID: entry.ID, Quantity: entry.Quantity})
	}
	if entries == nil {
		entries = []giftDefinitionEntry{}
	}
	return entries, total, nil
}

func kindLabel(kind gamecatalog.Kind) string {
	if kind == gamecatalog.KindPet {
		return "宠物"
	}
	return "物品"
}

func decodeStoredGiftDefinition(raw json.RawMessage) (giftDefinition, error) {
	var definition giftDefinition
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&definition); err != nil {
		return giftDefinition{}, errors.New("礼包定义无效")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return giftDefinition{}, errors.New("礼包定义无效")
	}
	return definition, nil
}

func (server *Server) normalizedStoredPackage(ctx context.Context, packageValue auth.GiftPackage) (auth.GiftPackage, json.RawMessage, error) {
	definition, err := decodeStoredGiftDefinition(packageValue.Definition)
	if err != nil {
		return auth.GiftPackage{}, nil, err
	}
	normalized, err := server.normalizeGiftDefinition(ctx, definition)
	if err != nil {
		return auth.GiftPackage{}, nil, err
	}
	packageValue.Definition = normalized
	snapshot, err := makeGiftPackageSnapshot(packageValue)
	if err != nil {
		return auth.GiftPackage{}, nil, err
	}
	return packageValue, snapshot, nil
}

func (server *Server) giftCharactersAPI(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
		return
	}
	account, err := server.resolveGiftAccount(request.Context(), request.URL.Query().Get("account_id"), request.URL.Query().Get("account_username"))
	if err != nil {
		server.giftAccountError(response, err)
		return
	}
	if server.players == nil {
		playerJSONError(response, http.StatusServiceUnavailable, "玩家管理服务尚未配置")
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
	playerJSON(response, http.StatusOK, map[string]any{
		"account_id":       account.ID,
		"account_username": account.Username,
		"characters":       characters,
	})
}

func (server *Server) resolveGiftAccount(ctx context.Context, rawID, rawUsername string) (auth.Account, error) {
	rawID = strings.TrimSpace(rawID)
	rawUsername = strings.TrimSpace(rawUsername)
	if rawID == "" && rawUsername == "" {
		return auth.Account{}, errors.New("请提供账号 ID 或账号名")
	}
	var byID, byName auth.Account
	var err error
	if rawID != "" {
		id, parseErr := strconv.ParseInt(rawID, 10, 64)
		if parseErr != nil || id <= 0 {
			return auth.Account{}, errors.New("账号 ID 无效")
		}
		byID, err = server.store.GetAccount(ctx, id)
		if err != nil {
			return auth.Account{}, auth.ErrNotFound
		}
	}
	if rawUsername != "" {
		byName, err = server.store.GetAccountByUsername(ctx, rawUsername)
		if err != nil {
			return auth.Account{}, auth.ErrNotFound
		}
	}
	if byID.ID != 0 && byName.ID != 0 && byID.ID != byName.ID {
		return auth.Account{}, errors.New("账号 ID 与账号名不匹配")
	}
	if byID.ID != 0 {
		return byID, nil
	}
	return byName, nil
}

func (server *Server) giftAccountError(response http.ResponseWriter, err error) {
	if errors.Is(err, auth.ErrNotFound) {
		playerJSONError(response, http.StatusNotFound, "账号不存在")
		return
	}
	playerJSONError(response, http.StatusBadRequest, err.Error())
}

func (server *Server) parseGiftRunRequest(request *http.Request) (giftRunRequest, error) {
	var payload giftRunRequest
	if err := decodeGiftJSON(request, &payload); err != nil {
		return giftRunRequest{}, err
	}
	if payload.PackageID <= 0 {
		return giftRunRequest{}, errors.New("礼包 ID 无效")
	}
	if payload.Scope != auth.GiftTargetSingle && payload.Scope != auth.GiftTargetAll {
		return giftRunRequest{}, errors.New("发放范围无效")
	}
	if payload.Scope == auth.GiftTargetSingle {
		if payload.CharacterSlot == nil || *payload.CharacterSlot < 0 || *payload.CharacterSlot > 1 {
			return giftRunRequest{}, errors.New("角色槽位无效")
		}
		if payload.AccountID == nil && strings.TrimSpace(payload.AccountUsername) == "" {
			return giftRunRequest{}, errors.New("单个角色发放需要账号 ID 或账号名")
		}
		if payload.AccountID != nil && *payload.AccountID <= 0 {
			return giftRunRequest{}, errors.New("账号 ID 无效")
		}
	} else if payload.AccountID != nil || strings.TrimSpace(payload.AccountUsername) != "" || payload.CharacterSlot != nil {
		return giftRunRequest{}, errors.New("全部角色发放不需要单个账号参数")
	}
	return payload, nil
}

func (server *Server) giftPreviewAPI(response http.ResponseWriter, request *http.Request, data *pageData) {
	if request.Method != http.MethodPost {
		playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
		return
	}
	if !server.giftMutationAllowed(request, data) {
		playerJSONError(response, http.StatusForbidden, "请求校验失败，请刷新页面")
		return
	}
	payload, err := server.parseGiftRunRequest(request)
	if err != nil {
		giftJSONDecodeError(response, err)
		return
	}
	if payload.PreviewToken != "" {
		playerJSONError(response, http.StatusBadRequest, "预览请求不能带确认令牌")
		return
	}
	packageValue, snapshot, err := server.loadGiftPackageSnapshot(request.Context(), payload.PackageID)
	if err != nil {
		server.giftPreviewError(response, err)
		return
	}
	if server.players == nil {
		playerJSONError(response, http.StatusServiceUnavailable, "玩家管理服务尚未配置")
		return
	}
	targets, skipped, eligible, binding, err := server.buildGiftTargets(request.Context(), payload, packageValue)
	if err != nil {
		server.giftPreviewError(response, err)
		return
	}
	if len(targets) == 0 {
		playerJSONError(response, http.StatusBadRequest, "没有找到可预览的角色")
		return
	}
	result := giftPreviewResponse{Targets: len(targets), Eligible: eligible, Skipped: skipped}
	if eligible > 0 {
		preview, token, tokenErr := server.newGiftPreview(data.Session.AdminUserID, binding, payload.Scope, payload.PackageID, snapshot, targets)
		if tokenErr != nil {
			playerJSONError(response, http.StatusInternalServerError, "无法创建预览令牌")
			return
		}
		result.PreviewToken = token
		_ = preview
	}
	playerJSON(response, http.StatusOK, result)
}

func (server *Server) loadGiftPackageSnapshot(ctx context.Context, packageID int64) (auth.GiftPackage, json.RawMessage, error) {
	packageValue, err := server.store.GetGiftPackage(ctx, packageID)
	if err != nil {
		return auth.GiftPackage{}, nil, err
	}
	return server.normalizedStoredPackage(ctx, packageValue)
}

func (server *Server) giftPreviewError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrNotFound):
		playerJSONError(response, http.StatusNotFound, "礼包不存在")
	case errors.Is(err, playerdata.ErrUnavailable):
		playerJSONError(response, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, playerdata.ErrNotFound):
		playerJSONError(response, http.StatusNotFound, "角色不存在")
	default:
		playerJSONError(response, http.StatusBadRequest, err.Error())
	}
}

func (server *Server) buildGiftTargets(ctx context.Context, payload giftRunRequest, packageValue auth.GiftPackage) ([]auth.GiftTarget, []giftPreviewSkipped, int, giftPreviewBinding, error) {
	definition, err := decodeStoredGiftDefinition(packageValue.Definition)
	if err != nil {
		return nil, nil, 0, giftPreviewBinding{}, err
	}
	itemCount, petCount := giftDefinitionCounts(definition)
	binding := giftPreviewBinding{PackageID: payload.PackageID, Scope: payload.Scope}
	if payload.Scope == auth.GiftTargetSingle {
		account, resolveErr := server.resolveGiftAccount(ctx, strconv.FormatInt(pointerInt64Value(payload.AccountID), 10), payload.AccountUsername)
		if payload.AccountID == nil {
			account, resolveErr = server.resolveGiftAccount(ctx, "", payload.AccountUsername)
		}
		if resolveErr != nil {
			return nil, nil, 0, giftPreviewBinding{}, resolveErr
		}
		binding.AccountID = account.ID
		binding.CharacterSlot = *payload.CharacterSlot
		snapshotValue, getErr := server.players.Get(ctx, account.Username, binding.CharacterSlot)
		if getErr != nil {
			return nil, nil, 0, giftPreviewBinding{}, getErr
		}
		target, skipped, ok := giftTargetForSnapshot(account, binding.CharacterSlot, snapshotValue, itemCount, petCount)
		if !ok {
			return []auth.GiftTarget{target}, []giftPreviewSkipped{skipped}, 0, binding, nil
		}
		return []auth.GiftTarget{target}, nil, 1, binding, nil
	}

	targets := make([]auth.GiftTarget, 0)
	skipped := make([]giftPreviewSkipped, 0)
	cursor := ""
	for {
		page, pageErr := server.store.ListAccountsPage(ctx, cursor, 100)
		if pageErr != nil {
			return nil, nil, 0, giftPreviewBinding{}, pageErr
		}
		for _, account := range page.Accounts {
			characters, listErr := server.players.List(ctx, account.Username)
			if listErr != nil {
				return nil, nil, 0, giftPreviewBinding{}, listErr
			}
			for _, character := range characters {
				if character.Slot < 0 || character.Slot > 1 {
					return nil, nil, 0, giftPreviewBinding{}, errors.New("玩家服务返回了无效角色槽位")
				}
				snapshotValue, getErr := server.players.Get(ctx, account.Username, character.Slot)
				if getErr != nil {
					return nil, nil, 0, giftPreviewBinding{}, getErr
				}
				target, skippedEntry, ok := giftTargetForSnapshot(account, character.Slot, snapshotValue, itemCount, petCount)
				targets = append(targets, target)
				if !ok {
					skipped = append(skipped, skippedEntry)
				}
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	eligible := len(targets) - len(skipped)
	return targets, skipped, eligible, binding, nil
}

func pointerInt64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func giftDefinitionCounts(definition giftDefinition) (int, int) {
	items, pets := 0, 0
	for _, entry := range definition.Items {
		items += entry.Quantity
	}
	for _, entry := range definition.Pets {
		pets += entry.Quantity
	}
	return items, pets
}

func giftTargetForSnapshot(account auth.Account, slot int, snapshot playerdata.Snapshot, itemCount, petCount int) (auth.GiftTarget, giftPreviewSkipped, bool) {
	target := auth.GiftTarget{AccountID: account.ID, CharacterSlot: slot, CharacterName: snapshot.Name}
	skipped := giftPreviewSkipped{AccountID: account.ID, Account: account.Username, CharacterSlot: slot, CharacterName: snapshot.Name}
	if strings.TrimSpace(snapshot.Name) == "" {
		target.SkipReason = "角色名称为空，无法绑定目标"
		skipped.Reason = target.SkipReason
		return target, skipped, false
	}
	itemFree, petFree := giftFreeCapacity(snapshot)
	reasons := make([]string, 0, 2)
	if itemCount > itemFree {
		reasons = append(reasons, fmt.Sprintf("物品背包容量不足（需要 %d，剩余 %d）", itemCount, itemFree))
	}
	if petCount > petFree {
		reasons = append(reasons, fmt.Sprintf("宠物栏容量不足（需要 %d，剩余 %d）", petCount, petFree))
	}
	if len(reasons) != 0 {
		target.SkipReason = strings.Join(reasons, "；")
		skipped.Reason = target.SkipReason
		return target, skipped, false
	}
	return target, skipped, true
}

func giftFreeCapacity(snapshot playerdata.Snapshot) (int, int) {
	itemCapacity, petCapacity := maxGiftItemCount, maxGiftPetCount
	if value, ok := snapshot.Capacities["item_inventory"]; ok && value >= 0 && value <= maxGiftItemCount {
		itemCapacity = value
	}
	if value, ok := snapshot.Capacities["pet_inventory"]; ok && value >= 0 && value <= maxGiftPetCount {
		petCapacity = value
	}
	itemUsed, petUsed := 0, 0
	for _, possession := range snapshot.Possessions {
		if possession.Location != "inventory" {
			continue
		}
		switch possession.Kind {
		case "item":
			// Slots 0..4 are equipment and do not consume gift backpack slots.
			if possession.Slot >= 5 && possession.Slot < 5+itemCapacity {
				itemUsed++
			}
		case "pet":
			if possession.Slot >= 0 && possession.Slot < petCapacity {
				petUsed++
			}
		}
	}
	if itemUsed > itemCapacity {
		itemUsed = itemCapacity
	}
	if petUsed > petCapacity {
		petUsed = petCapacity
	}
	return itemCapacity - itemUsed, petCapacity - petUsed
}

func (server *Server) newGiftPreview(adminID int64, binding giftPreviewBinding, scope string, packageID int64, packageSnapshot json.RawMessage, targets []auth.GiftTarget) (giftPreview, string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return giftPreview{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	now := server.giftClockNow()
	preview := giftPreview{
		Token:           token,
		AdminID:         adminID,
		PackageID:       packageID,
		Scope:           scope,
		PackageSnapshot: append(json.RawMessage(nil), packageSnapshot...),
		Targets:         cloneGiftTargets(targets),
		Binding:         binding,
		CreatedAt:       now,
		ExpiresAt:       now.Add(giftPreviewLifetime),
	}
	server.giftMu.Lock()
	defer server.giftMu.Unlock()
	server.pruneGiftPreviewsLocked(now)
	for len(server.giftPreviews) >= maxGiftPreviews {
		server.removeOldestGiftPreviewLocked()
	}
	if server.giftPreviews == nil {
		server.giftPreviews = make(map[string]*giftPreview)
	}
	copyPreview := preview
	server.giftPreviews[token] = &copyPreview
	return preview, token, nil
}

func cloneGiftTargets(targets []auth.GiftTarget) []auth.GiftTarget {
	return append([]auth.GiftTarget(nil), targets...)
}

func (server *Server) pruneGiftPreviewsLocked(now time.Time) {
	for token, preview := range server.giftPreviews {
		if preview == nil || !preview.ExpiresAt.After(now) {
			delete(server.giftPreviews, token)
		}
	}
}

func (server *Server) removeOldestGiftPreviewLocked() {
	var oldestToken string
	var oldest time.Time
	for token, preview := range server.giftPreviews {
		if preview == nil {
			oldestToken = token
			break
		}
		if oldestToken == "" || preview.CreatedAt.Before(oldest) {
			oldestToken, oldest = token, preview.CreatedAt
		}
	}
	if oldestToken != "" {
		delete(server.giftPreviews, oldestToken)
	}
}

func (server *Server) giftRunsAPI(response http.ResponseWriter, request *http.Request, data *pageData) {
	switch request.Method {
	case http.MethodGet:
		limit, err := giftPageLimit(request.URL.Query().Get("limit"))
		if err != nil {
			playerJSONError(response, http.StatusBadRequest, err.Error())
			return
		}
		page, err := server.store.ListGiftRuns(request.Context(), request.URL.Query().Get("cursor"), limit)
		if err != nil {
			playerJSONError(response, http.StatusBadRequest, err.Error())
			return
		}
		runs := make([]giftRunView, 0, len(page.Runs))
		for _, run := range page.Runs {
			runs = append(runs, makeGiftRunView(run))
		}
		playerJSON(response, http.StatusOK, map[string]any{"runs": runs, "next_cursor": page.NextCursor})
	case http.MethodPost:
		if !server.giftMutationAllowed(request, data) {
			playerJSONError(response, http.StatusForbidden, "请求校验失败，请刷新页面")
			return
		}
		payload, err := server.parseGiftRunRequest(request)
		if err != nil {
			giftJSONDecodeError(response, err)
			return
		}
		if payload.PreviewToken == "" {
			playerJSONError(response, http.StatusBadRequest, "请先生成发放预览")
			return
		}
		run, repeated, err := server.confirmGiftRun(request.Context(), data.Session.AdminUserID, payload)
		if err != nil {
			server.giftConfirmError(response, err)
			return
		}
		if !repeated {
			server.startGiftWorker()
		}
		status := http.StatusCreated
		if repeated {
			status = http.StatusOK
		}
		playerJSON(response, status, map[string]any{"run": makeGiftRunView(run)})
	default:
		playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
	}
}

func giftPageLimit(raw string) (int, error) {
	if raw == "" {
		return 100, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 500 {
		return 0, errors.New("每页数量须为 1–500")
	}
	return limit, nil
}

func (server *Server) giftRunAPI(response http.ResponseWriter, request *http.Request, data *pageData) {
	if request.Method != http.MethodGet {
		playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
		return
	}
	id, ok := parseGiftPathID(strings.TrimSuffix(request.URL.Path, "/"), "/api/gift-runs/")
	if !ok {
		playerJSONError(response, http.StatusNotFound, "发放任务不存在")
		return
	}
	limit, err := giftPageLimit(request.URL.Query().Get("limit"))
	if err != nil {
		playerJSONError(response, http.StatusBadRequest, err.Error())
		return
	}
	details, err := server.store.GetGiftRunDetails(request.Context(), id, request.URL.Query().Get("cursor"), limit)
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			playerJSONError(response, http.StatusNotFound, "发放任务不存在")
		} else {
			playerJSONError(response, http.StatusBadRequest, err.Error())
		}
		return
	}
	playerJSON(response, http.StatusOK, map[string]any{
		"run":         makeGiftRunView(details.Run),
		"deliveries":  details.Deliveries,
		"next_cursor": details.NextCursor,
	})
}

func (server *Server) confirmGiftRun(ctx context.Context, adminID int64, payload giftRunRequest) (auth.GiftRun, bool, error) {
	now := server.giftClockNow()
	server.giftMu.Lock()
	defer server.giftMu.Unlock()
	server.pruneGiftPreviewsLocked(now)
	preview, ok := server.giftPreviews[payload.PreviewToken]
	if !ok || preview == nil {
		return auth.GiftRun{}, false, errGiftPreviewExpired
	}
	if preview.AdminID != adminID {
		return auth.GiftRun{}, false, errGiftPreviewOwner
	}
	binding, err := server.bindingForRequest(ctx, payload)
	if err != nil {
		return auth.GiftRun{}, false, err
	}
	if binding != preview.Binding || payload.Scope != preview.Scope || payload.PackageID != preview.PackageID {
		return auth.GiftRun{}, false, errors.New("确认参数与预览不一致，请重新预览")
	}
	if preview.RunID > 0 {
		run, err := server.store.GetGiftRun(ctx, preview.RunID)
		return run, true, err
	}
	packageValue, snapshot, err := server.loadGiftPackageSnapshot(ctx, payload.PackageID)
	if err != nil {
		return auth.GiftRun{}, false, err
	}
	if !jsonEqual(snapshot, preview.PackageSnapshot) {
		return auth.GiftRun{}, false, errors.New("礼包内容已变化，请重新预览")
	}
	if err := server.recordGiftRunIntent(ctx, adminID, payload, preview); err != nil {
		return auth.GiftRun{}, false, err
	}
	// CreateGiftRun atomically snapshots this exact package and all previewed
	// targets before the background worker can claim a delivery.
	run, err := server.store.CreateGiftRun(ctx, packageValue.ID, payload.Scope, adminID, cloneGiftTargets(preview.Targets))
	if err != nil {
		return auth.GiftRun{}, false, err
	}
	preview.RunID = run.ID
	return run, false, nil
}

func (server *Server) recordGiftRunIntent(ctx context.Context, adminID int64, payload giftRunRequest, preview *giftPreview) error {
	if preview == nil {
		return errGiftPreviewExpired
	}
	skipped := 0
	for _, target := range preview.Targets {
		if strings.TrimSpace(target.SkipReason) != "" {
			skipped++
		}
	}
	detail := mustJSON(struct {
		PackageID   int64  `json:"package_id"`
		Scope       string `json:"scope"`
		TargetCount int    `json:"target_count"`
		Eligible    int    `json:"eligible"`
		Skipped     int    `json:"skipped"`
	}{payload.PackageID, payload.Scope, len(preview.Targets), len(preview.Targets) - skipped, skipped})
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := server.store.RecordAudit(auditCtx, giftAdminID(adminID), "gift_run_intent", "", "", string(detail)); err != nil {
		return fmt.Errorf("无法记录发放意图，未创建任务：%w", err)
	}
	return nil
}

func jsonEqual(a, b json.RawMessage) bool {
	return string(a) == string(b)
}

func (server *Server) bindingForRequest(ctx context.Context, payload giftRunRequest) (giftPreviewBinding, error) {
	binding := giftPreviewBinding{PackageID: payload.PackageID, Scope: payload.Scope}
	if payload.Scope == auth.GiftTargetAll {
		return binding, nil
	}
	account, err := server.resolveGiftAccount(ctx, strconv.FormatInt(pointerInt64Value(payload.AccountID), 10), payload.AccountUsername)
	if payload.AccountID == nil {
		account, err = server.resolveGiftAccount(ctx, "", payload.AccountUsername)
	}
	if err != nil {
		return giftPreviewBinding{}, err
	}
	binding.AccountID = account.ID
	binding.CharacterSlot = *payload.CharacterSlot
	return binding, nil
}

func (server *Server) giftConfirmError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errGiftPreviewExpired):
		playerJSONError(response, http.StatusGone, err.Error())
	case errors.Is(err, errGiftPreviewOwner):
		playerJSONError(response, http.StatusForbidden, err.Error())
	case errors.Is(err, auth.ErrNotFound), errors.Is(err, playerdata.ErrNotFound):
		playerJSONError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, playerdata.ErrUnavailable):
		playerJSONError(response, http.StatusServiceUnavailable, err.Error())
	default:
		playerJSONError(response, http.StatusConflict, err.Error())
	}
}

func (server *Server) startGiftWorker() {
	server.giftMu.Lock()
	if server.giftWorkerRunning {
		server.giftWorkerKick = true
		server.giftMu.Unlock()
		return
	}
	server.giftWorkerRunning = true
	server.giftWorkerKick = false
	server.giftMu.Unlock()
	go server.giftWorker()
}

func (server *Server) giftWorker() {
	ctx := context.Background()
	server.giftMu.Lock()
	recoverAtStartup := !server.giftRecoveryDone
	server.giftMu.Unlock()
	if recoverAtStartup {
		if _, err := server.store.RecoverInterruptedGiftDeliveries(ctx); err != nil {
			log.Printf("gift startup recovery failed: %v", err)
			server.giftMu.Lock()
			server.giftWorkerRunning = false
			server.giftWorkerKick = false
			server.giftMu.Unlock()
			return
		}
		server.giftMu.Lock()
		server.giftRecoveryDone = true
		server.giftMu.Unlock()
	}
	for {
		delivery, ok, err := server.store.ClaimNextGiftDelivery(ctx)
		if err != nil {
			log.Printf("gift delivery claim failed: %v", err)
			break
		}
		if !ok {
			server.giftMu.Lock()
			kick := server.giftWorkerKick
			server.giftWorkerKick = false
			if !kick {
				server.giftWorkerRunning = false
				server.giftMu.Unlock()
				return
			}
			server.giftMu.Unlock()
			continue
		}
		server.processGiftDelivery(ctx, delivery)
	}
	server.giftMu.Lock()
	server.giftWorkerRunning = false
	server.giftWorkerKick = false
	server.giftMu.Unlock()
}

func (server *Server) processGiftDelivery(ctx context.Context, delivery auth.GiftDelivery) {
	// A single broken game target must not hold the queue forever. The manager
	// has its own shorter online-save timeout, while this outer deadline also
	// bounds account reads and a malformed bridge response.
	processCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	run, err := server.store.GetGiftRun(processCtx, delivery.RunID)
	if err != nil {
		server.finishGiftDelivery(processCtx, delivery, auth.GiftDeliveryFailed, nil, nil, "无法读取发放任务："+err.Error())
		return
	}
	definition, err := decodeStoredGiftDefinition(run.Definition)
	if err != nil {
		server.finishGiftDelivery(processCtx, delivery, auth.GiftDeliveryFailed, nil, nil, err.Error())
		return
	}
	bundle := giftBundle(definition)
	account, err := server.store.GetAccount(processCtx, delivery.AccountID)
	if err != nil {
		server.finishGiftDelivery(processCtx, delivery, auth.GiftDeliveryFailed, nil, nil, "账号不存在")
		return
	}
	intent := struct {
		RunID         int64                    `json:"run_id"`
		DeliveryID    int64                    `json:"delivery_id"`
		AccountID     int64                    `json:"account_id"`
		CharacterSlot int                      `json:"character_slot"`
		CharacterName string                   `json:"character_name"`
		Bundle        []playerdata.BundleEntry `json:"bundle"`
	}{run.ID, delivery.ID, delivery.AccountID, delivery.CharacterSlot, delivery.CharacterName, bundle}
	intentBytes, _ := json.Marshal(intent)
	actor := giftAdminID(run.AdminID)
	if auditErr := server.store.RecordAudit(processCtx, actor, "gift_delivery_intent", account.Username, "", string(intentBytes)); auditErr != nil {
		server.finishGiftDelivery(processCtx, delivery, auth.GiftDeliveryFailed, nil, nil, "无法记录发放意图，未执行发放："+auditErr.Error())
		return
	}
	if server.players == nil {
		server.finishGiftDelivery(processCtx, delivery, auth.GiftDeliveryFailed, nil, nil, "玩家管理服务尚未配置")
		return
	}
	snapshot, err := server.players.Get(processCtx, account.Username, delivery.CharacterSlot)
	if err != nil {
		server.finishGiftDelivery(processCtx, delivery, auth.GiftDeliveryFailed, nil, nil, err.Error())
		return
	}
	if strings.TrimSpace(delivery.CharacterName) == "" || snapshot.Name != delivery.CharacterName {
		server.finishGiftDelivery(processCtx, delivery, auth.GiftDeliveryFailed, nil, nil, "角色名称已变化，已拒绝发放")
		return
	}
	itemCount, petCount := giftDefinitionCounts(definition)
	if itemFree, petFree := giftFreeCapacity(snapshot); itemCount > itemFree || petCount > petFree {
		reason := fmt.Sprintf("容量不足（物品需要 %d、剩余 %d；宠物需要 %d、剩余 %d）", itemCount, itemFree, petCount, petFree)
		server.finishGiftDelivery(processCtx, delivery, auth.GiftDeliverySkipped, nil, nil, reason)
		return
	}
	mutation := playerdata.Mutation{Revision: snapshot.Revision, Action: "grant_bundle", Location: "inventory", Bundle: bundle}
	result, err := server.players.Apply(processCtx, account.Username, delivery.CharacterSlot, mutation)
	if err != nil {
		status := giftApplyErrorStatus(err)
		server.finishGiftDelivery(processCtx, delivery, status, mustJSON(mutation), nil, err.Error())
		return
	}
	resultBytes, _ := json.Marshal(map[string]any{"revision": result.Revision})
	server.finishGiftDelivery(processCtx, delivery, auth.GiftDeliveryApplied, mustJSON(mutation), resultBytes, "")
}

// giftApplyErrorStatus keeps the delivery queue conservative when the native
// game service rejects a bundle. Typed bridge refusals are final outcomes: a
// capacity error can be safely recorded as a per-character skip, while the
// remaining known validations and rollback paths are failed. Unknown errors,
// including an interrupted save, remain uncertain because the mutation may
// have reached the game process. No status returned here is retried.
func giftApplyErrorStatus(err error) string {
	if errors.Is(err, playerdata.ErrConflict) || errors.Is(err, playerdata.ErrNotFound) {
		return auth.GiftDeliveryFailed
	}
	var bridgeErr *playerbridge.Error
	if !errors.As(err, &bridgeErr) {
		return auth.GiftDeliveryUncertain
	}
	switch bridgeErr.Code {
	case "inventory_full", "pet_full", "warehouse_full":
		return auth.GiftDeliverySkipped
	case "ambiguous_target", "expected_revision_required", "stale_target",
		"busy_battle", "busy_trade", "not_connected", "target_not_online",
		"invalid_template", "bundle_create_failed", "bundle_required",
		"invalid_quantity", "invalid_bundle", "template_required",
		"item_not_found", "stale_item", "equipped_item", "invalid_item_field",
		"invalid_item_name", "invalid_item_quantity", "pet_create_failed",
		"pet_not_found", "stale_pet", "invalid_pet_name", "invalid_pet_field",
		"invalid_pet_growth", "invalid_pet_value", "pet_skill_slots_in_use",
		"invalid_character_field", "invalid_character_value", "invalid_skill_slot",
		"invalid_skill", "slot_required", "unknown_action", "save_unavailable",
		"export_failed", "inspect_failed":
		return auth.GiftDeliveryFailed
	case "mutation_applied_save_failed":
		return auth.GiftDeliveryUncertain
	default:
		return auth.GiftDeliveryUncertain
	}
}

func giftBundle(definition giftDefinition) []playerdata.BundleEntry {
	bundle := make([]playerdata.BundleEntry, 0, len(definition.Items)+len(definition.Pets))
	for _, entry := range definition.Items {
		bundle = append(bundle, playerdata.BundleEntry{Kind: string(gamecatalog.KindItem), TemplateID: entry.ID, Quantity: entry.Quantity})
	}
	for _, entry := range definition.Pets {
		bundle = append(bundle, playerdata.BundleEntry{Kind: string(gamecatalog.KindPet), TemplateID: entry.ID, Quantity: entry.Quantity})
	}
	return bundle
}

func giftAdminID(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func (server *Server) finishGiftDelivery(ctx context.Context, delivery auth.GiftDelivery, status string, payload, result json.RawMessage, deliveryError string) {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	completed, err := server.store.CompleteGiftDelivery(persistCtx, delivery.ID, status, payload, result, deliveryError)
	if err != nil {
		log.Printf("gift delivery completion failed delivery_id=%d status=%s: %v", delivery.ID, status, err)
		return
	}
	actorRun, runErr := server.store.GetGiftRun(persistCtx, completed.RunID)
	if runErr != nil {
		return
	}
	account, accountErr := server.store.GetAccount(persistCtx, completed.AccountID)
	if accountErr != nil {
		return
	}
	detail := mustJSON(struct {
		RunID      int64  `json:"run_id"`
		DeliveryID int64  `json:"delivery_id"`
		Status     string `json:"status"`
		Error      string `json:"error,omitempty"`
	}{completed.RunID, completed.ID, completed.Status, completed.Error})
	if auditErr := server.store.RecordAudit(persistCtx, giftAdminID(actorRun.AdminID), "gift_delivery_"+completed.Status, account.Username, "", string(detail)); auditErr != nil {
		log.Printf("gift delivery result audit failed delivery_id=%d: %v", completed.ID, auditErr)
	}
}
