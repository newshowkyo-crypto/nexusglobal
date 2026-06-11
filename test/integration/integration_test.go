package integration

import (
	"testing"
)

func TestPayoutFivePhaseFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Log("Payout five-phase flow: freeze → FXLock → Payout → Confirm → Reconcile")
	t.Log("TODO: implement with testcontainers")
}

func TestWebhookToLedgerPosting(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Log("Webhook → Streams → Ledger: duplicate webhook should not duplicate entry")
	t.Log("TODO: implement with testcontainers")
}

func TestSweepWindowAndDelayQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Log("Sweep: outside window → Delay Queue, inside window → submit")
	t.Log("TODO: implement with testcontainers")
}

func TestConcurrentDoubleSpend(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Log("Concurrent 5 payout requests with same balance: only covered range succeeds, no oversell")
	t.Log("TODO: implement with testcontainers")
}
