// Copyright 2024 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !nonfsfs && !nofilesystem

package collector

import (
	"io"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/procfs"
)

// --- Unit tests for the pure helper function ---

func TestExtractNFSOpStats_ReadWrite(t *testing.T) {
	ops := []procfs.NFSOperationStats{
		{Operation: "NULL", Requests: 5},
		{Operation: "READ", Requests: 100, CumulativeTotalRequestMilliseconds: 5000},
		{Operation: "WRITE", Requests: 50, CumulativeTotalRequestMilliseconds: 2500},
		{Operation: "ACCESS", Requests: 200},
	}
	read, write := extractNFSOpStats(ops)

	if read.requests != 100 {
		t.Errorf("read.requests: want 100, got %d", read.requests)
	}
	if read.timeMs != 5000 {
		t.Errorf("read.timeMs: want 5000, got %d", read.timeMs)
	}
	if write.requests != 50 {
		t.Errorf("write.requests: want 50, got %d", write.requests)
	}
	if write.timeMs != 2500 {
		t.Errorf("write.timeMs: want 2500, got %d", write.timeMs)
	}
}

// Older kernels or NFS versions may omit READ/WRITE — collector must not fail.
func TestExtractNFSOpStats_MissingOps(t *testing.T) {
	ops := []procfs.NFSOperationStats{
		{Operation: "NULL", Requests: 3},
		{Operation: "GETATTR", Requests: 10},
	}
	read, write := extractNFSOpStats(ops)

	if read.requests != 0 || read.timeMs != 0 {
		t.Errorf("expected zero read stats for missing READ op, got requests=%d timeMs=%d", read.requests, read.timeMs)
	}
	if write.requests != 0 || write.timeMs != 0 {
		t.Errorf("expected zero write stats for missing WRITE op, got requests=%d timeMs=%d", write.requests, write.timeMs)
	}
}

func TestExtractNFSOpStats_Empty(t *testing.T) {
	read, write := extractNFSOpStats(nil)
	if read.requests != 0 || write.requests != 0 {
		t.Error("expected zero stats for nil ops slice")
	}
}

// --- Integration tests using proc fixture ---

type testNFSfsCollector struct {
	c Collector
}

func (tc testNFSfsCollector) Collect(ch chan<- prometheus.Metric) {
	tc.c.Update(ch) //nolint:errcheck
}

func (tc testNFSfsCollector) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(tc, ch)
}

func newTestNFSfsCollector(t *testing.T) Collector {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := NewNFSfsCollector(logger)
	if err != nil {
		t.Fatalf("NewNFSfsCollector: %v", err)
	}
	return c
}

func gatherNFSfs(t *testing.T, c Collector) []*dto.MetricFamily {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := reg.Register(testNFSfsCollector{c}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return mfs
}

// TestNFSfsCollector_SeriesCount verifies that the fixtures produce the expected
// number of metric families and series.
//
// fixtures/proc/10/mountstats contains 3 NFS mounts with unique (device,mountpoint,fstype):
//
//	192.168.1.1:/srv/test  /mnt/nfs/test        nfs4
//	192.168.1.1:/srv/test  /mnt/nfs/test-dupe   nfs4
//	192.168.1.1:/srv/test  /mnt/nfs/test-dupe   nfs
func TestNFSfsCollector_SeriesCount(t *testing.T) {
	*procPath = "fixtures/proc"

	c := newTestNFSfsCollector(t)
	mfs := gatherNFSfs(t, c)

	const wantFamilies = 6
	if len(mfs) != wantFamilies {
		var names []string
		for _, mf := range mfs {
			names = append(names, mf.GetName())
		}
		t.Errorf("expected %d metric families, got %d: %v", wantFamilies, len(mfs), names)
	}
	for _, mf := range mfs {
		if len(mf.Metric) != 3 {
			t.Errorf("metric family %q: expected 3 series, got %d", mf.GetName(), len(mf.Metric))
		}
	}
}

// TestNFSfsCollector_NFSv3 verifies that NFSv3 mounts (fstype=nfs) are collected.
func TestNFSfsCollector_NFSv3(t *testing.T) {
	*procPath = "fixtures/proc"

	c := newTestNFSfsCollector(t)
	mfs := gatherNFSfs(t, c)

	nfsFound := false
	for _, mf := range mfs {
		for _, m := range mf.Metric {
			for _, lp := range m.Label {
				if lp.GetName() == "fstype" && lp.GetValue() == "nfs" {
					nfsFound = true
				}
			}
		}
	}
	if !nfsFound {
		t.Error("expected at least one series with fstype=nfs")
	}
}

// TestNFSfsCollector_Filter verifies that mount points matched by the exclude
// regexp are omitted from the output.
func TestNFSfsCollector_Filter(t *testing.T) {
	*procPath = "fixtures/proc"
	origExclude := *mountPointsExclude
	*mountPointsExclude = "^/mnt/nfs/test$"
	defer func() { *mountPointsExclude = origExclude }()

	c := newTestNFSfsCollector(t)
	mfs := gatherNFSfs(t, c)

	// /mnt/nfs/test is excluded; /mnt/nfs/test-dupe (nfs + nfs4) remain → 2 per family.
	for _, mf := range mfs {
		if len(mf.Metric) != 2 {
			t.Errorf("metric family %q: expected 2 series after filter, got %d", mf.GetName(), len(mf.Metric))
		}
	}
}

// TestNFSfsCollector_Dedup checks the deduplication key struct equality directly.
func TestNFSfsCollector_Dedup(t *testing.T) {
	key1 := nfsMountKey{device: "server:/export", mountpoint: "/mnt/a", fstype: "nfs4"}
	key2 := nfsMountKey{device: "server:/export", mountpoint: "/mnt/a", fstype: "nfs4"}
	key3 := nfsMountKey{device: "server:/export", mountpoint: "/mnt/b", fstype: "nfs4"}

	seen := make(map[nfsMountKey]bool)
	seen[key1] = true

	if !seen[key2] {
		t.Error("identical key should be detected as duplicate")
	}
	if seen[key3] {
		t.Error("different mountpoint must not collide with existing key")
	}
}

// TestNFSfsCollector_NoUpdate verifies that a zero-mount environment does not
// cause Update to return an error.
func TestNFSfsCollector_NoError(t *testing.T) {
	*procPath = "fixtures/proc"

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := NewNFSfsCollector(logger)
	if err != nil {
		t.Fatalf("NewNFSfsCollector: %v", err)
	}

	ch := make(chan prometheus.Metric, 100)
	if err := c.Update(ch); err != nil {
		t.Errorf("Update returned unexpected error: %v", err)
	}
	close(ch)
}
