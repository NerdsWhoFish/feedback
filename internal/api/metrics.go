package api

import (
	"context"

	"github.com/NerdsWhoFish/feedback/internal/telemetry"
)

func (s *Server) SetMetrics(metrics *telemetry.Metrics) { s.metrics = metrics }

func (s *Server) RequestCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT status,count(*) FROM requests GROUP BY status`)
	if err != nil {
		s.log.ErrorContext(ctx, "request metrics collection failed")
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int64)
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}
