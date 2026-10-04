package agent

import (
	"context"
	"strings"
	"testing"
)

func TestClosedSuspender_Fails(t *testing.T) {
	s := ClosedSuspender("unattended test run")
	_, err := s(context.Background(), SuspendRequest{Kind: SuspendAsk, Prompt: "q?"})
	if err == nil || !strings.Contains(err.Error(), "ask_user") {
		t.Fatalf("want kind-naming error, got %v", err)
	}
}

func TestSuspendKinds_Distinct(t *testing.T) {
	if SuspendAsk == SuspendPermission || SuspendPermission == SuspendPlan || SuspendAsk == SuspendPlan ||
		SuspendBudget == SuspendAsk || SuspendBudget == SuspendPermission || SuspendBudget == SuspendPlan {
		t.Fatal("suspend kinds must be distinct")
	}
}
