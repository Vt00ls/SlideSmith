package runtimeexecution

import (
	"context"
	"database/sql"
)

type postgresSchedulerCancellationTransaction struct {
	tx       *sql.Tx
	function string
	fact     SchedulerCancellationFact
	called   bool
}

func (transaction *postgresSchedulerCancellationTransaction) AcceptCancellation(ctx context.Context) error {
	if transaction.called || ctx == nil || ctx.Err() != nil {
		return newError(ErrorDependencyUnavailable)
	}
	fact := transaction.fact
	if _, err := transaction.tx.ExecContext(ctx, "SELECT "+transaction.function+`(
		$1,$2,$3,$4,$5,$6,$7)`, fact.OperationID.String(), fact.CanonicalRequestDigest[:],
		fact.RuntimeRunID.String(), fact.DecisionID.String(), fact.RuntimeRevision,
		fact.RuntimeFence, fact.AcceptedAt); err != nil {
		return newError(ErrorIntegrityConflict)
	}
	transaction.called = true
	return nil
}

var _ SchedulerCancellationTransaction = (*postgresSchedulerCancellationTransaction)(nil)
