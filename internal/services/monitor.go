package services

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KilimcininKorOglu/lankeeper/internal/netutil"
)

type MonitorService struct {
	mu      sync.RWMutex
	current SystemStats
	history []SystemStats
	maxHist int

	prevCPUIdle  uint64
	prevCPUTotal uint64
	prevRx       map[string]uint64
	prevTx       map[string]uint64
	prevTime     time.Time
}

type SystemStats struct {
	Timestamp   time.Time
	CPUPercent  float64
	RAMTotal    uint64
	RAMUsed     uint64
	RAMPercent  float64
	Temperature float64
	Uptime      time.Duration
	Interfaces  map[string]IfaceStats
}

type IfaceStats struct {
	RxBytesPerSec uint64
	TxBytesPerSec uint64
	RxBytes       uint64
	TxBytes       uint64
}

func NewMonitorService() *MonitorService {
	return &MonitorService{
		maxHist: 60,
		prevRx:  make(map[string]uint64),
		prevTx:  make(map[string]uint64),
	}
}

func (s *MonitorService) Start(stop <-chan struct{}, ifaces []string) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			stats := s.collect(ifaces)
			s.mu.Lock()
			s.current = stats
			s.history = append(s.history, stats)
			if len(s.history) > s.maxHist {
				s.history = s.history[1:]
			}
			s.mu.Unlock()
		}
	}
}

func (s *MonitorService) GetCurrent() SystemStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

func (s *MonitorService) GetHistory() []SystemStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]SystemStats, len(s.history))
	copy(result, s.history)
	return result
}

func (s *MonitorService) collect(ifaces []string) SystemStats {
	now := time.Now()
	stats := SystemStats{
		Timestamp:  now,
		Interfaces: make(map[string]IfaceStats),
	}

	stats.CPUPercent = s.readCPU()
	stats.RAMTotal, stats.RAMUsed, stats.RAMPercent = readMemInfo()
	stats.Temperature = readTemperature()
	stats.Uptime = readUptime()

	elapsed := now.Sub(s.prevTime).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}

	for _, name := range ifaces {
		rx, tx, err := netutil.ReadInterfaceStats(name)
		if err != nil {
			continue
		}

		ifStats := IfaceStats{RxBytes: rx, TxBytes: tx}

		if prevRx, ok := s.prevRx[name]; ok {
			if rx >= prevRx {
				ifStats.RxBytesPerSec = uint64(float64(rx-prevRx) / elapsed)
			}
		}
		if prevTx, ok := s.prevTx[name]; ok {
			if tx >= prevTx {
				ifStats.TxBytesPerSec = uint64(float64(tx-prevTx) / elapsed)
			}
		}

		s.prevRx[name] = rx
		s.prevTx[name] = tx
		stats.Interfaces[name] = ifStats
	}

	s.prevTime = now
	return stats
}

func (s *MonitorService) readCPU() float64 {
	if runtime.GOOS != "linux" {
		return 0
	}

	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return 0
	}

	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0
	}

	var total, idle uint64
	for i := 1; i < len(fields); i++ {
		val, _ := strconv.ParseUint(fields[i], 10, 64)
		total += val
		if i == 4 {
			idle = val
		}
	}

	deltaTotal := total - s.prevCPUTotal
	deltaIdle := idle - s.prevCPUIdle
	s.prevCPUTotal = total
	s.prevCPUIdle = idle

	if deltaTotal == 0 {
		return 0
	}

	return float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
}

func readMemInfo() (total, used uint64, percent float64) {
	if runtime.GOOS != "linux" {
		return 0, 0, 0
	}

	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	defer func() { _ = f.Close() }()

	var memTotal, memAvailable uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			_, _ = fmt.Sscanf(line, "MemTotal: %d kB", &memTotal)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			_, _ = fmt.Sscanf(line, "MemAvailable: %d kB", &memAvailable)
		}
	}

	total = memTotal * 1024
	used = (memTotal - memAvailable) * 1024
	if memTotal > 0 {
		percent = float64(memTotal-memAvailable) / float64(memTotal) * 100
	}
	return
}

func readTemperature() float64 {
	if runtime.GOOS != "linux" {
		return 0
	}

	data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return 0
	}

	milliC, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return float64(milliC) / 1000.0
}

func readUptime() time.Duration {
	if runtime.GOOS != "linux" {
		return 0
	}

	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}

	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}

	secs, _ := strconv.ParseFloat(fields[0], 64)
	return time.Duration(secs * float64(time.Second))
}
