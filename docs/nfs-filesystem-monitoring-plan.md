## Plan: NFS 挂载点统一监控

保留现有 filesystem collector 负责 NFS 容量/占用，不改 diskstats 的块设备模型；新增一个 Linux-only、低基数的 NFS collector，从 /proc/self/mountstats + /proc/self/mountinfo 提取每个 NFS 挂载点的 IO 指标，并把标签对齐到普通磁盘常用的 device/mountpoint/fstype，这样看板上 NFS 挂载和本地磁盘能保持一致的使用体验。现有高级 mountstats collector 继续保留且默认关闭，用于深度排障。

**Steps**
1. Phase 1 - 定义指标面
   1. 不新增 NFS 容量/占用指标，直接复用现有 node_filesystem_size_bytes、node_filesystem_avail_bytes、node_filesystem_free_bytes 等，因为 Linux 下 NFS 挂载已由 filesystem collector 自动采集。
   2. 新增一组按挂载点暴露的 NFS IO 指标，推荐最小集合为：read_bytes_total、write_bytes_total、read_requests_total、write_requests_total、read_time_seconds_total、write_time_seconds_total。
   3. 标签统一为 device、mountpoint、fstype，保证 PromQL 和 Grafana 面板可以和普通文件系统维度对齐；协议、mountaddr、operation 等高基数标签不放进这组新指标。
2. Phase 2 - 新增 collector 骨架
   1. 在 collector 目录新增一个 Linux-only 的 NFS filesystem collector，并按现有 registerCollector 模式接入。
   2. 推荐这个新 collector 在该私有 fork 中 defaultEnabled；如果你更看重后续与 upstream 的 rebase 成本，则改为 defaultDisabled 并通过启动参数显式开启。
   3. collector 构造函数沿用现有 mountstats collector 的 procfs 打开方式，直接读取 /proc/self/mountstats 和 /proc/self/mountinfo。
3. Phase 3 - 按挂载点对齐身份
   1. 参考现有 mountstats collector 已验证的“mountstats 与 mountinfo 顺序一致”假设，将两份数据按索引关联。
   2. 仅保留 fstype 为 nfs/nfs4 的挂载，mountpoint 走和 filesystem collector 一样的 rootfsStripPrefix/标准化流程，device 直接使用 export 源地址，fstype 取自 mountinfo。
   3. 去重键改为 device + mountpoint + fstype，目标是每个可见挂载点只产生一组时间序列，而不是按 export/protocol/mountaddr 展开。
4. Phase 4 - 提取最小 IO 语义
   1. 读写字节数直接取 MountStatsNFS.Bytes.Read / Write。
   2. 读写请求数与累计时间从 READ / WRITE operation 统计中提取；实现上需要兼容缺少某些 operation 的内核或 NFS 版本，缺失时不要让整个 collector 失败。
   3. 不把 transport 维度、逐 operation 全量展开、RPC 细节搬进新 collector；这部分仍由现有 mountstats collector 负责，避免重复和高基数。
5. Phase 5 - 与现有 filesystem 行为保持一致
   1. 复用 filesystem 的挂载点/文件系统类型 include/exclude 过滤逻辑，让 NFS IO 与 NFS 容量/占用遵循同一套筛选规则。
   2. 文档里明确区分：filesystem = 容量/占用；新 NFS filesystem collector = 每挂载点 IO；mountstats = 高粒度 NFS 调试指标。
   3. 如果新 collector 不是默认开启，还要同步更新示例 systemd/sysconfig 启动参数。
6. Phase 6 - 测试与验证
   1. 把“挂载点对齐 + NFS 过滤 + 去重 + READ/WRITE 提取”拆成可单测的纯函数，优先用结构体/fixture 做单元测试，而不是把测试绑死在真实 /proc。
   2. 覆盖场景至少包括：NFSv3/NFSv4、同一 export 多次挂载、同一挂载存在多个 transport 条目、缺少 READ/WRITE operation、过滤规则命中/未命中。
   3. 代码层验证先跑 collector 相关测试，再跑 go test ./...；运行层验证在有真实 NFS 挂载的 Linux 主机上检查新旧指标是否能按 mountpoint 对齐，以及在实际读写负载下 rate() 是否变化。
   4. 灰度上线后额外观察 node_scrape_collector_duration_seconds 与 scrape_samples_post_metric_relabeling，确认新 collector 没有明显拉高抓取耗时和样本量。

**Relevant files**
- collector/filesystem_common.go — 现有 filesystem collector 的注册、标签定义、include/exclude 过滤逻辑。
- collector/filesystem_linux.go — Linux 挂载发现、NFS 未被默认排除、mountpoint 标准化逻辑。
- collector/mountstats_linux.go — 现有 /proc/self/mountstats 读取方式、与 mountinfo 的关联假设、NFS 字节/operation 数据来源。
- collector/nfs_linux.go — Linux-only NFS collector 的命名与注册风格参考。
- collector/collector.go — registerCollector/defaultEnabled/defaultDisabled 接入模式。
- README.md — collector 列表与启用说明，需要补充 NFS capacity vs IO 的使用说明。
- examples/systemd/node_exporter.service — 如果新 collector 走显式开启，需要加启动参数示例。
- examples/systemd/sysconfig.node_exporter — 如果新 collector 走显式开启，需要加环境变量示例。

**Verification**
1. 运行针对 collector 的单元测试，重点验证 NFS mountstats 到统一标签指标的映射。
2. 运行 go test ./...，确认 collector 注册和 Linux 条件编译没有引入回归。
3. 在带 NFS 挂载的 Linux 主机上人工核对：node_filesystem_* 已包含 fstype=nfs.* 的容量/占用，新指标按同一 mountpoint 输出 IO。
4. 施加真实读写负载后，确认 read/write bytes、requests、time 的 rate() 有明显变化，并与 nfsstat 或 /proc/self/mountstats 量级一致。
5. 观察 scrape 耗时与样本量，确保这个 slim collector 没有继承完整 mountstats collector 的高基数问题。

**Decisions**
- 已确认范围只做 Linux 主机上的 NFS 客户端挂载侧，不包含 NFS 服务端。
- 已确认目标是让 NFS 挂载在看板上的体验尽量接近普通磁盘，而不是简单暴露现有 mountstats 全量标签。
- 明确不改 diskstats，因为 /proc/diskstats 天生只覆盖块设备，NFS IO 应从 /proc/self/mountstats 获取。
- 明确不替换现有 mountstats collector；新 collector 只提供低基数、面向看板的按挂载点指标。

**Further Considerations**
1. 推荐把新 collector 设为该 fork 的默认开启项；如果你后续会频繁 rebase upstream，可改为默认关闭并在 Docker/systemd 参数中打开。
2. 推荐 Grafana 面板把本地磁盘 IO 与 NFS IO 视为两类 PromQL 数据源，但共享同一套 device/mountpoint/fstype 维度，而不是强行复用 node_disk_* 查询.
