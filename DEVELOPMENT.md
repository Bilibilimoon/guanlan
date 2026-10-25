# 观澜（guanlan）开发记录（面向后续维护者 / AI）

最后更新：2026-10-25，对应版本 **0.4.1**。发布渠道：GitHub Releases（tag 挂 `guanlan.fpk`）。

这份文档记录「为什么这么做」和「踩过哪些坑」，README.md 只讲用户怎么装怎么用。
接手前请先通读本文，再改代码。

---

## 1. 项目是什么

飞牛 fnOS 系统监控应用「观澜」：实时总览 NAS 各项资源（CPU/内存/NPU/GPU/温度/风扇/
网络/磁盘），分层存储历史曲线（10 分钟~1 年，滚轮缩放下钻），进程列表（只读），
阈值告警与磁盘健康监视。**仅 fnOS 管理员可用。**

单进程、自包含二进制（前端嵌入），不依赖 Node/Python/Docker/PHP。

明确不做的事：

- 不结束进程（用户决策：纯只读，无特权身份下能力也极有限）
- 不读 SMART 完整属性 / NPU·GPU 利用率百分比（需 root，见 §4.3）
- 不做公网中继穿透（fnConnect 不转发第三方端口，平台限制）
- TCP 端口只做局域网只读镜像，不做完整管理（见 §3）

## 2. 快速开始

```bash
# 构建（前端 → 嵌入 → 双架构 → fpk；需 Go≥1.23、Node≥18、fnpack）
# 本机若 Go 不在 PATH：export GUANLAN_GOBIN=<go 的 bin 目录>
./build.sh
# 产物：pkg/guanlan/guanlan.fpk 与本机架构 ./guanlan

# 单元测试（差分/存储/告警/鉴权共 17 例）
cd server && go test ./...

# 本机直跑开发实例（跳过管理员校验）
./guanlan -dev -data ./data    # http://IP:9780

# 装机（升级必须先卸载，fnpack 不支持原地升级）
sudo appcenter-cli uninstall guanlan
sudo appcenter-cli install-fpk pkg/guanlan/guanlan.fpk
sudo appcenter-cli start guanlan
```

装机版验证（socket 权限 0660，需 sudo）：

```bash
SOCK=/vol1/@appcenter/guanlan/guanlan.sock
sudo curl -s --unix-socket $SOCK http://localhost/api/healthz          # 免认证
sudo curl -s --unix-socket $SOCK -H 'X-Trim-Isadmin: true' .../api/realtime  # 200
sudo curl -s --unix-socket $SOCK .../api/realtime                      # 403
curl http://127.0.0.1:9780/api/realtime                                # TCP 只读 200
```

注意 `appcenter-cli start` 返回后 socket 未立即就绪，等 3~5 秒再 curl。

## 3. 运行时拓扑

```
 fnOS 桌面(管理员)          局域网浏览器/手机
      │ HTTPS               │ HTTP（默认 9780，应用中心可改）
      ▼                     ▼
 fnOS 统一网关           guanlan 进程 ── TCP listener（MarkTCP：只读 GET）
      │ Unix socket          ▲ 剥掉全部 X-Trim-* 头（防伪造管理员）
      ▼ 注入身份头            │
      └────────────► guanlan 进程 ── Unix socket listener
                        │（用户 guanlan，无特权，RSS ~20MB）
                        ├─ /proc /sys 采集（2s 默认）
                        ├─ 环形缓冲（15min，支持亚秒采样）
                        └─ SQLite（TRIM_PKGVAR/guanlan.db，WAL）
```

两条通道的权限矩阵（有单测覆盖，见 `api_test.go`）：

| 通道 | 只读 GET | 写操作（config/rules/ack/clear） |
|---|---|---|
| Unix socket + 管理员（`X-Trim-Isadmin: true`） | ✅ | ✅ |
| Unix socket + 普通用户 | ❌ 403 | ❌ 403 |
| TCP 直连（任何头都剥） | ✅ | ❌ 403 |

## 4. 关键架构决策

### 4.1 分层时序存储

`metrics(id,name)` 字典 + 三张同构窄表 `points_l0/l1/l2 (t,k,v, PK(k,t)) WITHOUT ROWID`。
落盘粒度（默认 15s）写 L0，每小时增量聚合 L0→L1（5min 桶）、L1→L2（1h 桶），游标
`ds_l1_pos/ds_l2_pos` 存 meta 断点续跑。保留天数可配（默认 30/180/365 天）。

为什么窄表不用宽表：序列数动态（网卡/温度传感器增减），窄表自适应；代价是体积略大，
但有 30 天 L0 × 35 序列 ≈ 数十 MB，可接受。`INSERT OR REPLACE` + 主键 (k,t) 保证
同秒覆盖幂等。

### 4.2 亚秒采样的时间戳

Sample.T 是 **float64 unix 秒**（`UnixNano()/1e9`）。曾经用整秒：0.5s/0.1s 采集间隔下
相邻样本时间戳相同，差分被跳过 → CPU/网络速率一半样本为 0、曲线闪烁。落盘时取整秒
（`int64(sm.T)`），库内仍是整秒对齐。

### 4.3 无特权采集面（实测边界）

- 可读：/proc 全部、thermal zones×7、drivetemp、pwmfan、devfreq 的
  `cur_freq/available_frequencies`、/sys/block 磁盘计数、ioerr_cnt、statfs
- **不可读**：debugfs 的 NPU/GPU 负载（`/sys/kernel/debug/rknpu/load`）、
  smartctl、/proc/pid/io（非同 uid）
- 替代：NPU/GPU 用「实时频率÷最高频率」+ 专属温度曲线做活动代理；
  进程级磁盘/网络如实标注「平台限制不可得」，Swap 用 /proc/pid/status 的 VmSwap

### 4.4 告警的持续判定

sustained = 环形缓冲内超限样本占比 ≥80%。**ring 只有 15 分钟**，所以 ForMin 上限 15
（前端/API/导入三处同步校验）。要支持更长持续窗口需查库，暂未做。

### 4.5 前端双数据源

窗口 ≤15min 走内存环形缓冲（`/api/ring`）+ realtime 增量追加；>15min 走
`/api/series`（按窗口分桶缓存 rowsByWindow），30s 整段校正。历史页滚轮缩放按可视
跨度动态换层（≤2d→L0，≤180d→L1，否则 L2），换层保留可视窗口。

### 4.6 公网 IP 探测

每小时经 `ip.3322.net / ifconfig.me / api.ip.sb / v6.ident.me / api64.ipify`
多源 fallback（HTTPS、5s 超时、响应按 IP 语法校验防劫持塞垃圾），Dialer 强制
tcp4/tcp6 各走各的出口。ipw.cn 曾可用但 DNS 已失效，不要加回去除非验证。

## 5. 数据与接口

序列命名：`cpu / mem_pct / load1..15 / swap_used / fan / gpu_pct / npu_pct /
temp.<zone|盘名> / net.<iface>.rx|tx / disk.<dev>.r|w / uptime`

接口（除 healthz 全部需鉴权，见 §3 矩阵）：`/api/healthz realtime ring series
meta config config/export config/import data/clear processes alerts alerts/rules
alerts/ack`。写操作一律要求 `X-Requested-With: guanlan`（防跨站表单）。

配置持久化在 SQLite `meta` 表（interval_ms/db_interval_ms/retain_*/alert_rules/
ds_*_pos），运行时可改即时生效。`data/clear` 要求 `{"confirm":"CLEAR"}` 双保险。

## 6. 前端结构

Vue3 + Vite + vue-i18n + ECharts 按需（gzip ~210KB）。页面懒加载分 chunk。
关键实现：

- **拖拽排序**：指针驱动 + 克隆幽灵 + transform 虚拟重排（拖动全程 DOM 不动，
  松手才提交数组）——不要改回「每次交换重排 DOM + FLIP」，会抽搐（实测）
- **tooltip 不随刷新消失**：`setOption` 用 `replaceMerge:['series']` 保留组件
  状态 + zrender 层记录悬停坐标兜底恢复
- **403 自愈**（api.js）：fnOS 网关会话过期会 403——先静默重试，仍 403 自动刷新
  一次（sessionStorage 限一次防循环）
- **列宽拖动**：`colresize.js` composable，localStorage 记忆
- 时间值进 ECharts 前统一 秒→毫秒（time 轴原生域），格式化 `fmtAxisMs` 按跨度自适应

## 7. 踩坑记录（按疼痛排序）

1. **`X-Trim-Isadmin` 的值是字符串 `true` 不是 `1`**——判错会把管理员全 403
   （v0.3.0 事故）。已写进 fnos-app-dev skill。
2. **TCP 通道伪造管理员头**：MarkTCP 必须剥 X-Trim-* 头，否则局域网任何人可写
   （v0.4.1 由单测抓出）。context 标记只读，请求侧无法伪造。
3. **读锁内回填缓存 map**：并发查询未知指标 → concurrent map writes panic。
   Query 不回填缓存（索引点查代价可接受）。
4. **fnpack 必需文件一次配齐**：config/privilege + config/resource({}) + cmd/ 九个
   全套脚本 + `app/ui/config`（**文件**非目录）。清单见 skill。
5. `pkill -f <模式>` 会杀死命令行含该模式的工具调用自身——用 `pkill -x`。
6. 运行中 `cp` 覆盖二进制报 Text file busy——先 stop。
7. pnpm 12 拦 esbuild postinstall（settings 已挪到 pnpm-workspace.yaml 仍拦）——
   build.sh 直接用系统 npm。
8. 工作区文件偶发 000 权限继承——构建前 `chmod -R u+rwX`。
9. gofmt 会重排对齐导致 Edit old_string 失配——批量改动用脚本（python）更稳。
10. fnOS 桌面 iframe 里页面用相对路径请求 API（base './'），兼容网关前缀。

## 8. 发版清单

1. `server/cmd/guanlan/main.go` 的 `appVersion` 与 `pkg/guanlan/manifest` 的
   `version` **同时**递增，manifest 更新 changelog。
2. `./build.sh`；`cd server && go test ./...` 全绿。
3. 卸载重装真机验证：healthz / 管理员 200 / 403 矩阵 / TCP 只读 / 版本号。
4. git 提交（过 git-privacy-check skill；fpk 是构建产物不进 git）。
5. GitHub Release 挂 fpk（推送前对 fpk 解包复扫隐私）。

## 9. 版本史

- 0.1.0 M1 骨架+采集+概览页
- 0.2.x M2 历史/独立页（未单独发版）
- 0.3.0 首个完整版（八页面+fpk）；0.3.1 修管理员误判
- 0.4.0 TCP 只读端口、进程页增强、导入导出、清空、403 自愈
- 0.4.1 bug 修复：并发 panic、TCP 伪造头、UTF-8 截断、告警窗口上限、文档
