package aiservice

import (
	"context"
	"errors"
	"github.com/k0ngk0ng/stoneage/internal/aiprovision"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"strings"
	"testing"
	"time"
)

func TestFactoryPreservesSafeProviderStage(t *testing.T) {
	root := factoryTestRoot(t)
	profile := factoryTestProfile("diagnostic-profile", false)
	provider := SessionProviderFunc(func(context.Context, airuntime.Profile) (SessionLease, error) {
		return SessionLease{}, aiprovision.WrapOpenFailure(profile.ID, "login_web_game", time.Now(), errors.New("password=secret-do-not-log"))
	})
	factory, err := NewFactory(newFactoryTestConfig(t, root, factoryTestModels{model: factoryTestModel(), defaultID: "model-1"}, factoryTestSecrets(t, root), provider))
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	_, err = factory.Open(context.Background(), profile)
	if !errors.Is(err, ErrFactorySession) || strings.Contains(err.Error(), "secret-do-not-log") {
		t.Fatalf("unsafe or incompatible diagnostic: %v", err)
	}
	var details interface {
		StartFailureDetails() (string, string, string, time.Duration)
	}
	if !errors.As(err, &details) {
		t.Fatal("provider stage lost at factory")
	}
	_, stage, _, _ := details.StartFailureDetails()
	if stage != "login_web_game" {
		t.Fatalf("stage=%s", stage)
	}
}
