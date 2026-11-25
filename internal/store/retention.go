package store

import (
	"context"
	"time"
)

// Compact rolls raw samples older than raw up into hourly rollups, deletes
// them, and deletes rollups, events and sessions past their retention.
// Only complete hours are rolled up, so it is safe to run repeatedly.
func (s *Store) Compact(ctx context.Context, raw, rollup time.Duration) error {
	now := s.now()
	cutoff := now.Add(-raw).Truncate(time.Hour).Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO client_rollups (network_id, mac, hour, samples, online_count, avg_up_bps, avg_down_bps, max_up_bps, max_down_bps)
		  SELECT network_id, mac, (ts/3600)*3600, COUNT(*), SUM(online), CAST(AVG(up_bps) AS INTEGER), CAST(AVG(down_bps) AS INTEGER), MAX(up_bps), MAX(down_bps)
		  FROM client_samples WHERE ts < ? GROUP BY network_id, mac, ts/3600
		  ON CONFLICT (network_id, mac, hour) DO UPDATE SET
		    avg_up_bps=(client_rollups.avg_up_bps*client_rollups.samples + excluded.avg_up_bps*excluded.samples)/(client_rollups.samples+excluded.samples),
		    avg_down_bps=(client_rollups.avg_down_bps*client_rollups.samples + excluded.avg_down_bps*excluded.samples)/(client_rollups.samples+excluded.samples),
		    max_up_bps=MAX(client_rollups.max_up_bps, excluded.max_up_bps), max_down_bps=MAX(client_rollups.max_down_bps, excluded.max_down_bps),
		    samples=client_rollups.samples+excluded.samples, online_count=client_rollups.online_count+excluded.online_count`, []any{cutoff}},
		{`DELETE FROM client_samples WHERE ts < ?`, []any{cutoff}},
		{`INSERT INTO node_rollups (network_id, node_id, hour, samples, online_count, avg_clients, avg_cpu, avg_mem)
		  SELECT network_id, node_id, (ts/3600)*3600, COUNT(*), SUM(online), AVG(clients), AVG(cpu), AVG(mem)
		  FROM node_samples WHERE ts < ? GROUP BY network_id, node_id, ts/3600
		  ON CONFLICT (network_id, node_id, hour) DO UPDATE SET
		    avg_clients=(node_rollups.avg_clients*node_rollups.samples + excluded.avg_clients*excluded.samples)/(node_rollups.samples+excluded.samples),
		    samples=node_rollups.samples+excluded.samples, online_count=node_rollups.online_count+excluded.online_count`, []any{cutoff}},
		{`DELETE FROM node_samples WHERE ts < ?`, []any{cutoff}},
		{`DELETE FROM client_rollups WHERE hour < ?`, []any{now.Add(-rollup).Unix()}},
		{`DELETE FROM node_rollups WHERE hour < ?`, []any{now.Add(-rollup).Unix()}},
		{`DELETE FROM events WHERE ts < ?`, []any{now.Add(-rollup).Unix()}},
		{`DELETE FROM sessions WHERE expires_at < ?`, []any{now.Unix()}},
	}
	for _, st := range stmts {
		if _, err := tx.ExecContext(ctx, st.q, st.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
