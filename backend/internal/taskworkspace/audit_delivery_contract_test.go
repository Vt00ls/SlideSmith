package taskworkspace

import (
	"context"
	"testing"
)

func TestAuditDeliveryDigestConflictQuarantinesAlertsAndDoesNotDeliver(t *testing.T) {
	persistence := NewInMemoryPersistence()
	evidence := CleanupAuditEvidence{
		ID:         "audit-fact-digest-conflict-1",
		Action:     CleanupAuditAcceptException,
		Resolution: CleanupAcceptedException,
		RecordedAt: 100,
	}
	evidence.Digest = evidence.CanonicalDigest()
	expected := auditDeliveryFact(evidence)
	conflicting := expected
	conflicting.Digest = canonicalDigest("different-audit-fact-content")
	persistence.cleanupAuditFacts[evidence.ID] = evidence
	persistence.auditDeliveries[evidence.ID] = auditDeliveryRecord{fact: conflicting}

	delivery := &auditDeliveryContractDouble{}
	alerts := &auditDeliveryAlertContractDouble{}
	lifecycle := NewInMemory(InMemoryConfig{
		Persistence:         persistence,
		AuditDelivery:       delivery,
		AuditDeliveryAlerts: alerts,
	})
	backlog, err := lifecycle.RebuildAuditDelivery(
		context.Background(), AuditDeliveryRebuildRequest{},
	)
	if err != nil {
		t.Fatalf("rebuild audit delivery: %v", err)
	}
	if delivery.calls != 0 || !backlog.Pending.Known || backlog.Pending.Value != 0 ||
		!backlog.Delivered.Known || backlog.Delivered.Value != 0 ||
		!backlog.Quarantined.Known || backlog.Quarantined.Value != 1 ||
		len(backlog.Evidence) != 1 || !backlog.Evidence[0].Quarantined ||
		backlog.Evidence[0].SafeError != SafeErrorIntegrityUnavailableContent {
		t.Fatalf("digest conflict was not quarantined safely: backlog=%#v delivery=%d", backlog, delivery.calls)
	}
	if len(alerts.alerts) != 1 || alerts.alerts[0].AuditFactID != expected.AuditFactID ||
		alerts.alerts[0].SafeError != SafeErrorIntegrityUnavailableContent {
		t.Fatalf("digest conflict integrity alert = %#v", alerts.alerts)
	}
}

type auditDeliveryContractDouble struct {
	calls int
}

func (d *auditDeliveryContractDouble) Deliver(context.Context, AuditDeliveryFact) error {
	d.calls++
	return nil
}

type auditDeliveryAlertContractDouble struct {
	alerts []AuditDeliveryIntegrityAlert
}

func (d *auditDeliveryAlertContractDouble) AlertAuditDeliveryIntegrity(
	_ context.Context,
	alert AuditDeliveryIntegrityAlert,
) error {
	d.alerts = append(d.alerts, alert)
	return nil
}
