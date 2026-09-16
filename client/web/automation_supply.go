package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aiservice"
)

func validateWebSupply(mode aicontrol.Mode, supply *AutomationSupply) error {
	if supply == nil {
		return nil
	}
	if mode != aicontrol.Leveling {
		return errors.New("自动补给仅适用于自动练级")
	}
	if supply.Item == "" || strings.TrimSpace(supply.Item) != supply.Item || len(supply.Item) > 128 ||
		supply.TargetCount < 2 || supply.TargetCount > 13 || supply.ReorderCount < 1 || supply.ReorderCount >= supply.TargetCount {
		return errors.New("请选择补给品，补满数量须为2–13，返店阈值须为1至补满数量减1")
	}
	return nil
}

func (executor *AutomationExecutor) validateSupplyOffer(supply *AutomationSupply) error {
	if supply == nil {
		return nil
	}
	for _, offer := range aiservice.ReviewedStockOffers(executor.config.Knowledge, executor.config.StockItems) {
		if offer.Alias == supply.Item {
			return nil
		}
	}
	return errors.New("所选补给品未安装或审核资料已失效")
}

func webSupplyParameters(supply *AutomationSupply) map[string]json.RawMessage {
	if supply == nil {
		return nil
	}
	item, _ := json.Marshal(supply.Item)
	target, _ := json.Marshal(supply.TargetCount)
	reorder, _ := json.Marshal(supply.ReorderCount)
	return map[string]json.RawMessage{"supply_item": item, "supply_target_count": target, "supply_reorder_count": reorder}
}

func (executor *AutomationExecutor) supplyPreviewProblems(ctx context.Context, session *AutomationSession, request AutomationStartRequest, binding aimcp.Binding, snapshot aigame.Snapshot) []string {
	if request.Config.Supply == nil {
		return nil
	}
	if !snapshot.AI.Received || !snapshot.AI.ItemsKnown {
		return []string{"背包资料尚未同步，无法核对补给"}
	}
	handle, err := executor.buildWebAutomationHandle(ctx, session, request, binding)
	if err != nil {
		return []string{"补给配置暂不可用"}
	}
	defer handle.closeBackend()
	stock, ok := handle.leveling.Supplies.(*aiservice.LevelingStock)
	if !ok {
		return []string{"自动补给能力未安装"}
	}
	order, err := stock.Preview(ctx, snapshot, webSupplyParameters(request.Config.Supply))
	if err != nil {
		return []string{"当前无法补给，请检查人物状态、背包空位和补给配置（须保留2个空位）"}
	}
	if order == nil {
		return nil
	}
	var problems []string
	cost := order.Action.MaximumCost
	if cost > request.Config.Budget.MaximumSpend {
		problems = append(problems, fmt.Sprintf("花费上限不足以授权一次补满（最多%d石币）", cost))
	}
	if int64(snapshot.Player.Gold)-request.Config.Budget.Reserve < cost {
		problems = append(problems, "保留金额以外的石币不足以授权一次补满")
	}
	return problems
}
