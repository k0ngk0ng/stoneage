package main

import (
	"context"
	"fmt"

	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
	"github.com/k0ngk0ng/stoneage/internal/aiprovision"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
	"github.com/k0ngk0ng/stoneage/internal/playermanager"
)

type initialPlayerState struct{ manager *playermanager.Manager }

func (i initialPlayerState) DefaultMount(ctx context.Context) (aiinitial.Pet, error) {
	return i.manager.DefaultAIMount(ctx)
}

// New admin creations ride by default. Resolve the birth-mode mount before
// provisioning reserves an identity, and never change explicit pet choices.
func (adapter *aiPlayerProvisionerAdapter) initialRequest(ctx context.Context, requested *aiinitial.Request) (*aiinitial.Request, error) {
	request := aiinitial.Request{Mode: "birth"}
	if requested != nil {
		request = *requested
	}
	if request.Mount == nil {
		enabled := true
		request.Mount = &enabled
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.Mode == "birth" && *request.Mount && len(request.Pets) == 0 {
		provider, ok := adapter.initializer.(interface {
			DefaultMount(context.Context) (aiinitial.Pet, error)
		})
		if !ok {
			return nil, fmt.Errorf("默认骑乘初始化服务尚未配置")
		}
		pet, err := provider.DefaultMount(ctx)
		if err != nil {
			return nil, err
		}
		request.Pets = []aiinitial.Pet{pet}
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return &request, nil
}

func (i initialPlayerState) Validate(ctx context.Context, p aiinitial.Resolved) error {
	return i.manager.ValidateInitial(ctx, p)
}
func (i initialPlayerState) Apply(ctx context.Context, account string, slot int, p aiinitial.Resolved) (playerdata.Snapshot, error) {
	return i.manager.InitializeAI(ctx, account, slot, p)
}

func (i initialPlayerState) Verify(ctx context.Context, account string, slot int, plan aiinitial.Resolved) (playerdata.Snapshot, error) {
	return i.manager.VerifyAIInitial(ctx, account, slot, plan)
}

func (adapter *aiPlayerProvisionerAdapter) ListInitialRecoveries(ctx context.Context) ([]aiprovision.InitialRecoveryView, error) {
	return adapter.provisioner.ListInitialRecoveries(ctx)
}
func (adapter *aiPlayerProvisionerAdapter) RecoverInitial(ctx context.Context, id, actor string, actorID *int64) (airuntime.Profile, error) {
	verifier, ok := adapter.initializer.(aiprovision.InitialRecoveryVerifier)
	if !ok {
		return airuntime.Profile{}, aiprovision.ErrInitialUnconfirmed
	}
	return adapter.provisioner.RecoverInitial(ctx, id, actor, actorID, verifier)
}
