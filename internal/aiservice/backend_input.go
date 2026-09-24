package aiservice

import (
	"context"
	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
)

// BackendInput is the server-owned context for deterministic automation.
type BackendInput struct {
	CharacterBuild *characterbuild.Policy
	Binding        aimcp.Binding
	Gate           *aicontrol.Gate
	Session        GameSession
	Tasks          TaskController
	Funding        FundingLookup
	Knowledge      *aiknowledge.Knowledge
	Receipts       *ReceiptStore
	Lease          context.Context
	Wake           chan<- struct{}
}

type BackendBuilder func(context.Context, BackendInput) (aimcp.Backend, error)
