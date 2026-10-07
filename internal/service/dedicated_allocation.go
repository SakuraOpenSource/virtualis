package service

import (
	"context"
	"gorm.io/gorm"
	"strings"
	"time"
)

// createReservationTransaction 只重试已回滚的数据库锁竞争，不重试远程创建。
func (s *VirtualisService) createReservationTransaction(ctx context.Context, fn func(*gorm.DB) error) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := s.db.WithContext(ctx).Transaction(fn)
		if err == nil {
			return nil
		}
		msg := strings.ToLower(err.Error())
		retryable := strings.Contains(msg, "database is locked") || strings.Contains(msg, "sqlite_busy") || strings.Contains(msg, "deadlock") || strings.Contains(msg, "serialization failure")
		if !retryable || attempt >= 31 {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
