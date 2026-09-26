package services

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

// parseByteSize reads a size such as "100M". The suffixes K, M and G are
// powers of 1024; a bare number is bytes.
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, strings.TrimSuffix(s, "G")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * mult, nil
}

// parseRetention reads a retention such as "7d", or any Go duration.
func parseRetention(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid retention %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid retention %q", s)
	}
	return d, nil
}

// rotateQueryLog keeps Unbound's query log within maxSize and drops the
// previous generation once it is older than retention. Unbound appends
// to the file it holds open, so the live file is copied and then
// emptied in place rather than renamed; a rename would leave Unbound
// writing to the old name. Both steps go through the agent, because the
// log belongs to unbound.
func (s *DNSService) rotateQueryLog(ctx context.Context) error {
	qc := s.cfg.DNS.QueryLog
	if !qc.Enabled || qc.LogPath == "" {
		return nil
	}
	if err := s.expireRotatedQueryLog(ctx, qc.LogPath+".1", qc.Retention); err != nil {
		return err
	}
	limit, err := parseByteSize(qc.MaxSize)
	if err != nil {
		return err
	}
	info, err := os.Stat(qc.LogPath)
	if err != nil || info.Size() <= limit {
		return nil
	}
	if _, err := netutil.Run(ctx, "cp", "--", qc.LogPath, qc.LogPath+".1"); err != nil {
		return fmt.Errorf("keep previous query log: %w", err)
	}
	if err := netutil.WriteFile(qc.LogPath, nil, 0o640); err != nil {
		return fmt.Errorf("empty query log: %w", err)
	}
	return nil
}

// expireRotatedQueryLog removes the previous generation once it is older
// than the retention.
func (s *DNSService) expireRotatedQueryLog(ctx context.Context, path, retention string) error {
	keep, err := parseRetention(retention)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) <= keep {
		return nil
	}
	if _, err := netutil.Run(ctx, "rm", "-f", "--", path); err != nil {
		return fmt.Errorf("expire previous query log: %w", err)
	}
	return nil
}
