package op

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func TestBuildChannelModelSyncPlan(t *testing.T) {
	existing := []model.ChannelModel{
		{ID: 1, Name: "manual-model", Source: model.ChannelModelSourceManual},
		{ID: 2, Name: "old-auto", Source: model.ChannelModelSourceAuto},
		{ID: 3, Name: "kept-auto", Source: model.ChannelModelSourceAuto},
	}
	fetched := map[string][]model.ChannelFetchModel{
		"key-a": {
			{Name: "manual-model", Protocols: model.ProtocolOpenAIChatCompletion},
			{Name: "new-auto", Protocols: model.ProtocolOpenAIChatCompletion},
			{Name: "kept-auto", Protocols: model.ProtocolAnthropicMessage},
		},
	}
	plan := buildChannelModelSyncPlan(existing, fetched, []string{"key-a"})
	if len(plan.create) != 1 || plan.create[0] != "new-auto" {
		t.Fatalf("create = %v", plan.create)
	}
	if len(plan.deleteIDs) != 1 || plan.deleteIDs[0] != 2 {
		t.Fatalf("deleteIDs = %v", plan.deleteIDs)
	}
	if _, ok := plan.desired["manual-model"]; ok {
		t.Fatalf("manual model must not be managed")
	}
	if got := plan.desired["new-auto"]["key-a"]; got != model.ProtocolOpenAIChatCompletion {
		t.Fatalf("new-auto protocols = %d", got)
	}
	if got := plan.desired["kept-auto"]["key-a"]; got != model.ProtocolAnthropicMessage {
		t.Fatalf("kept-auto protocols = %d", got)
	}
}

func TestBuildChannelModelSyncPlanFailedKeyUntouched(t *testing.T) {
	existing := []model.ChannelModel{{ID: 1, Name: "m", Source: model.ChannelModelSourceAuto}}
	fetched := map[string][]model.ChannelFetchModel{
		"key-a": {{Name: "m", Protocols: model.ProtocolOpenAIChatCompletion}},
	}
	plan := buildChannelModelSyncPlan(existing, fetched, []string{"key-a"})
	if _, ok := plan.desired["m"]["key-b"]; ok {
		t.Fatalf("failed key must not receive grants")
	}
}