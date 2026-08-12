package auditmaintenance

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const leaseCleanupTimeout = 5 * time.Second

// ErrMaintenanceBusy reports an active file lock, lease, or SQLite writer.
var ErrMaintenanceBusy = errors.New("audit maintenance is already running")

// ErrLeaseLost reports that another owner replaced this run's database lease.
var ErrLeaseLost = errors.New("audit maintenance lease was lost")

type maintenanceLease struct {
	owner string
	runID string
	ttl   time.Duration
}

func (lease maintenanceLease) acquireTransaction(
	ctx context.Context,
	transaction *sql.Tx,
	now time.Time,
) error {
	expiresAt := now.Add(lease.ttl).UTC().Format(time.RFC3339Nano)
	result, err := transaction.ExecContext(ctx, acquireLeaseSQL,
		lease.owner, lease.runID, expiresAt, now.UTC().Format(time.RFC3339Nano),
	)
	return inspectLeaseAcquisition(result, err)
}

const acquireLeaseSQL = `
	insert into audit_maintenance_lease(singleton, owner, run_id, expires_at)
	values (1, ?, ?, ?)
	on conflict(singleton) do update set
		owner = excluded.owner,
		run_id = excluded.run_id,
		expires_at = excluded.expires_at
	where julianday(audit_maintenance_lease.expires_at) <= julianday(?)
`

func inspectLeaseAcquisition(result sql.Result, err error) error {
	if err != nil {
		return classifyMaintenanceWriteError("acquire audit maintenance lease", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return wrapError("inspect audit maintenance lease acquisition", err)
	}
	if changed != 1 {
		return ErrMaintenanceBusy
	}
	return nil
}

func (lease maintenanceLease) renew(
	ctx context.Context,
	database *sql.DB,
	now time.Time,
) error {
	result, err := database.ExecContext(ctx, `
		update audit_maintenance_lease set expires_at = ?
		where singleton = 1 and owner = ? and run_id = ?
	`, now.Add(lease.ttl).UTC().Format(time.RFC3339Nano), lease.owner, lease.runID)
	if err != nil {
		return classifyMaintenanceWriteError("renew audit maintenance lease", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return wrapError("inspect audit maintenance lease renewal", err)
	}
	if changed != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (lease maintenanceLease) release(ctx context.Context, database *sql.DB) error {
	_, err := database.ExecContext(ctx, `
		delete from audit_maintenance_lease
		where singleton = 1 and owner = ? and run_id = ?
	`, lease.owner, lease.runID)
	if err != nil {
		return classifyMaintenanceWriteError("release audit maintenance lease", err)
	}
	return nil
}

func releaseLeaseBounded(
	ctx context.Context,
	database *sql.DB,
	lease maintenanceLease,
) error {
	cleanupContext, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		leaseCleanupTimeout,
	)
	defer cancel()
	for {
		err := lease.release(cleanupContext, database)
		if !errors.Is(err, ErrMaintenanceBusy) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-cleanupContext.Done():
			timer.Stop()
			return wrapError("release audit maintenance lease", cleanupContext.Err())
		case <-timer.C:
		}
	}
}
