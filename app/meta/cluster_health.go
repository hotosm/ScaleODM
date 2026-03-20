package meta

import "context"

func (s *Store) HealthCheck(ctx context.Context) error {
	return s.db.HealthCheck(ctx)
}
