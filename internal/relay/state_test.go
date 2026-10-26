package relay

import (
	"fmt"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func TestRequestRoundsKeepHistory(t *testing.T) {
	request := &RequestState{}
	for round := 1; round <= 25; round++ {
		request.startRound(func() {}, "ch key", fmt.Sprintf("model-%d", round), model.ProtocolOpenAIChatCompletion)
		request.finishRound(fmt.Sprintf("error-%d", round))
	}
	if len(request.Rounds) != maxRounds {
		t.Fatalf("rounds = %d, want %d", len(request.Rounds), maxRounds)
	}
	last := request.Rounds[len(request.Rounds)-1]
	if last.Round != 25 || last.Error != "error-25" || last.Sending {
		t.Fatalf("last round = %+v", last)
	}
	foundPrevious := false
	for _, round := range request.Rounds {
		if round.Round == 24 && round.Error == "error-24" {
			foundPrevious = true
		}
	}
	if !foundPrevious {
		t.Fatalf("previous failure was dropped: %+v", request.Rounds)
	}
}

func TestRequestRoundsSnapshotImmutable(t *testing.T) {
	request := &RequestState{}
	request.startRound(func() {}, "ch key", "model", model.ProtocolOpenAIChatCompletion)
	snapshot := request.Rounds
	request.finishRound("boom")
	if snapshot[len(snapshot)-1].Error != "" {
		t.Fatalf("finishRound mutated an existing snapshot")
	}
}
