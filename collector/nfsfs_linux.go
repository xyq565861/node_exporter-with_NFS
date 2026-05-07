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
	"fmt"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/procfs"
)

type nfsfsCollector struct {
	readBytesTotal        *prometheus.Desc
	writeBytesTotal       *prometheus.Desc
	readRequestsTotal     *prometheus.Desc
	writeRequestsTotal    *prometheus.Desc
	readTimeSecondsTotal  *prometheus.Desc
	writeTimeSecondsTotal *prometheus.Desc

	mountPointFilter deviceFilter
	fsTypeFilter     deviceFilter

	proc   procfs.Proc
	logger *slog.Logger
}

type nfsMountKey struct {
	device     string
	mountpoint string
	fstype     string
}

// nfsOpStats holds the per-operation READ/WRITE counters extracted from MountStatsNFS.
type nfsOpStats struct {
	requests uint64
	timeMs   uint64
}

func init() {
	registerCollector("nfsfs", defaultEnabled, NewNFSfsCollector)
}

// NewNFSfsCollector returns a Collector exposing per-NFS-mount IO statistics.
// Labels are aligned with the filesystem collector (device/mountpoint/fstype) so
// that NFS capacity metrics and IO metrics can be joined in PromQL by mountpoint.
func NewNFSfsCollector(logger *slog.Logger) (Collector, error) {
	fs, err := procfs.NewFS(*procPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open procfs: %w", err)
	}

	proc, err := fs.Self()
	if err != nil {
		return nil, fmt.Errorf("failed to open /proc/self: %w", err)
	}

	mountPointFilter, err := newMountPointsFilter(logger)
	if err != nil {
		return nil, fmt.Errorf("unable to parse mount points filter flags: %w", err)
	}

	fsTypeFilter, err := newFSTypeFilter(logger)
	if err != nil {
		return nil, fmt.Errorf("unable to parse fs types filter flags: %w", err)
	}

	const subsystem = "nfsfs"
	labels := []string{"device", "mountpoint", "fstype"}

	return &nfsfsCollector{
		readBytesTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "read_bytes_total"),
			"Number of bytes read from the NFS server, per mount point.",
			labels, nil,
		),
		writeBytesTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "write_bytes_total"),
			"Number of bytes written to the NFS server, per mount point.",
			labels, nil,
		),
		readRequestsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "read_requests_total"),
			"Number of NFS READ RPC requests, per mount point.",
			labels, nil,
		),
		writeRequestsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "write_requests_total"),
			"Number of NFS WRITE RPC requests, per mount point.",
			labels, nil,
		),
		readTimeSecondsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "read_time_seconds_total"),
			"Cumulative time spent waiting for NFS READ responses, per mount point, in seconds.",
			labels, nil,
		),
		writeTimeSecondsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "write_time_seconds_total"),
			"Cumulative time spent waiting for NFS WRITE responses, per mount point, in seconds.",
			labels, nil,
		),
		mountPointFilter: mountPointFilter,
		fsTypeFilter:     fsTypeFilter,
		proc:             proc,
		logger:           logger,
	}, nil
}

func (c *nfsfsCollector) Update(ch chan<- prometheus.Metric) error {
	mounts, err := c.proc.MountStats()
	if err != nil {
		return fmt.Errorf("failed to parse mountstats: %w", err)
	}

	seen := make(map[nfsMountKey]bool)

	for _, m := range mounts {
		stats, ok := m.Stats.(*procfs.MountStatsNFS)
		if !ok {
			continue
		}

		fstype := m.Type
		if fstype != "nfs" && fstype != "nfs4" {
			continue
		}

		device := m.Device
		mountpoint := rootfsStripPrefix(m.Mount)

		if c.mountPointFilter.ignored(mountpoint) {
			c.logger.Debug("Ignoring NFS mount point", "mountpoint", mountpoint)
			continue
		}
		if c.fsTypeFilter.ignored(fstype) {
			c.logger.Debug("Ignoring NFS fs type", "fstype", fstype)
			continue
		}

		key := nfsMountKey{device: device, mountpoint: mountpoint, fstype: fstype}
		if seen[key] {
			c.logger.Debug("Skipping duplicate NFS mount", "device", device, "mountpoint", mountpoint)
			continue
		}
		seen[key] = true

		read, write := extractNFSOpStats(stats.Operations)
		lv := []string{device, mountpoint, fstype}

		ch <- prometheus.MustNewConstMetric(c.readBytesTotal, prometheus.CounterValue, float64(stats.Bytes.Read), lv...)
		ch <- prometheus.MustNewConstMetric(c.writeBytesTotal, prometheus.CounterValue, float64(stats.Bytes.Write), lv...)
		ch <- prometheus.MustNewConstMetric(c.readRequestsTotal, prometheus.CounterValue, float64(read.requests), lv...)
		ch <- prometheus.MustNewConstMetric(c.writeRequestsTotal, prometheus.CounterValue, float64(write.requests), lv...)
		ch <- prometheus.MustNewConstMetric(c.readTimeSecondsTotal, prometheus.CounterValue, float64(read.timeMs)/1000.0, lv...)
		ch <- prometheus.MustNewConstMetric(c.writeTimeSecondsTotal, prometheus.CounterValue, float64(write.timeMs)/1000.0, lv...)
	}

	return nil
}

// extractNFSOpStats finds READ and WRITE entries in the operations slice.
// Missing operations return zero values so the collector does not fail on
// older kernels or NFS versions that omit certain operations.
func extractNFSOpStats(ops []procfs.NFSOperationStats) (read, write nfsOpStats) {
	for _, op := range ops {
		switch op.Operation {
		case "READ":
			read = nfsOpStats{
				requests: op.Requests,
				timeMs:   op.CumulativeTotalRequestMilliseconds,
			}
		case "WRITE":
			write = nfsOpStats{
				requests: op.Requests,
				timeMs:   op.CumulativeTotalRequestMilliseconds,
			}
		}
	}
	return
}
