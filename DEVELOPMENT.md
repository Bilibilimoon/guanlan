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
11. **表头列宽拖动**：`table-layout:fixed` 下表格**必须有显式 width**（如
    width:100%），否则 Chromium 直接忽略 col 的像素宽（渲染成内容宽）；拖动
    过程直改 `<col>` 元素 style.width（不依赖响应式链路），松手才回写状态。
12. **前端改动的验证要用真实浏览器**：本机 headless Chromium + playwright-core
    （executablePath=/usr/bin/chromium）可直接驱动 dev 实例做点击/拖拽断言。
    三个坑：a) 装机版占 9780，dev 实例必须换端口，否则 curl/测试打的是生产版
    （403→自愈逻辑无限刷新，现象极具迷惑性）；b) 只 `npm run build` 不重编 Go
    的话 embed 里还是旧产物（症状：改了代码测试结果分毫不差）；c) Vue 内联
    事件传多个参数时 handler 签名必须对应（`thCursor(col,e)` 写成
    `(e)` 会拿字符串当事件，静默抛错）。
13. **移动端 tooltip 生命周期**：触摸端 ECharts 不派发 globalout，浮窗会一直
    挂着；且"数据刷新恢复"机制会把触摸浮窗不断顶回来。方案：lastPos 只在
    pointerType==='mouse' 时记录；canvas 的 pointerup/pointercancel（非鼠标）
    与图表外 pointerdown 立即 hideTip。注意 headless 里 ECharts tooltip 根本
    不显示（新旧版本皆然，真机正常）——tooltip 类改动只能真机验证，headless
    断言会全部假阴性。
14. **.gitignore 无斜杠模式匹配任意层级**：`guanlan` 意在排除根目录构建产物，
    却把 `pkg/guanlan/`、`server/cmd/guanlan/` 整目录忽略——源文件静默缺失。
    排除根级产物用 `/guanlan` 锚定；仓库完整性用 `git ls-files` 对比磁盘核查。

15. **功耗只能估算**：RK3588 板无功率计——hwmon 只有 thermal/pwmfan/drivetemp，
    Type-C PD 控制器（tcpm_source_psy）在 DC 供电时 online=0 全 0，无 RAPL/SCMI
    powercap，12V 输入电压只在 root 的 debugfs（regulator_summary）且 current 列
    恒为 0mA，设备树无 INA 传感器。故 `collect/power.go` 用经验模型：基线 6W +
    CPU 8W×占用 + GPU/NPU（扣 20% 最低频底噪后）+ 每盘固定功耗（按
    `/sys/block/*/queue/rotational` 区分机械/固态）+ I/O 忙碌增量。UI 必须标注
    估算与误差，不得伪装成实测。
16. **ECharts setOption 的合并残留**：切换指标组只换 series 时，上一组的
    `yAxis.max`（如百分比组的 100）会粘在新轴上，把 11~13W 的功耗曲线压成一条
    贴底线。`setOption(opt, { replaceMerge: ['series', 'yAxis'] })` 一并替换轴。
    同理 **localStorage 里存的卡片顺序不会包含后来新增的卡片**——加载时按默认
    顺序补齐缺失 id（`mergeOrder`），否则新卡片永远不显示。

17. **ECharts 按需引入必须注册 dataZoom**：`echarts.use([LineChart, GridComponent,
    LegendComponent, TooltipComponent, CanvasRenderer])` 少了
    `DataZoomInsideComponent` 时，`dataZoom: [{type:'inside', zoomOnMouseWheel:true}]`
    不报任何错但完全不生效——滚轮在有 tooltip 的图表上翻不动时间轴（v0.5.9 用户
    反馈）。按需引入下"配置合法但交互缺失"是静默失败，改交互务必真机/浏览器验证。
18. **ECharts inside 缩放不以鼠标为中心**：内置数据缩放固定以视图中心为锚点，没有
    "以鼠标为中心"的选项（v0.5.10 用户反馈）。只能关掉 `zoomOnMouseWheel` 自己接管
    wheel：用 `convertFromPixel` 取鼠标处时间，保持它在视窗中的相对位置不变，反推
    `dispatchAction({type:'dataZoom', start, end})`。两个实现坑：a) **zrender 在 canvas
    上接管 wheel 并在冒泡阶段吞掉事件**，监听必须用 `{ capture: true }` 绑在容器上；
    b) `convertFromPixel({xAxisIndex:0}, px)` 返回**标量**（time 轴为毫秒），传数组或
    按数组取值会拿到 NaN 静默失效。
19. **滚轮缩放/切档的三个连带坑**：a) `chart.getOption().dataZoom[0]` 在百分比驱动下
    `startValue/endValue` 是空的，直接使用得到 NaN，会向下游发 `start=NaN`（后端
    400）——要用 start/end 百分比乘当前数据时间范围换算成秒；b) 切档位时的
    `dispatchAction({type:'dataZoom',start:0,end:100})` 会合成一次 dataZoom 事件，
    把刚切的 L1 按"可视跨度小"又拉回 L0，需短暂守卫（800ms）忽略；c) 快速切档
    时并发请求晚到的会覆盖新结果（高亮与内容不符）——加请求序号，只应用最新一次。
20. **历史页窗口档位按分组区分**（v0.5.12 → v0.5.13）：温度/网络/磁盘 = 概览细档
    加历史长档合并成 11 档，占用/功耗/负载/风扇保留长窗口 6 档。两套档位各自需要
    独立的当前值（`fineSec` / `coarseSec`）并在该组切回时沿用，否则切一次组就把别的组
    选的档位覆盖。合并时注意同一时段只留一个按钮（1小时、24小时=1天），否则
    `:key` 重复且两个按钮同时高亮。实现上 `rangeSec` 变成 computed（取当前组的档位），
    因此在 `loadWindow`（滚轮换层后回写窗口）里**不能**再写 `rangeSec.value`——必须先
    判组，写回对应那套的 ref，否则 Vue 直接报 "computed value is readonly"。
    档位变多后 `.seg` 必须加 `flex-wrap: wrap; max-width: 100%`，不然窄屏会横向溢出。
22. **TCP 直连的 Basic Auth 放在 `MarkTCP` 内层**（v0.5.15）：认证中间件必须能读到
    `MarkTCP` 写进 context 的 `tcpROKey`，所以顺序是
    `MarkTCP → authTCP → gzip → mux`（`New` 返回 `securityHeaders(authTCP(gzip(mux)))`）。
    三个坑：a) `/api/healthz` 必须显式放行（红线：健康检查不能要认证，否则网关探活失败）；
    b) 未配置 `auth_user` 时必须放行，否则升级后老用户直接被锁死；c) 凭据只能在
    `/api/config`（管理员通道）写，TCP 通道是只读 GET，天然改不了密码。
    密码用加盐 SHA-256 迭代 5 万次（不引依赖），校验结果按 Authorization 头摘要缓存
    15 分钟——否则 2 秒一次的轮询会反复跑 10ms 级别的哈希。
23. **磁盘空间守卫（v0.5.15）**：`statfs` 判定的是"数据目录所在盘"（`Bavail*Bsize`），
    每 20 秒缓存一次；`statfs` 失败时按空间充足处理（宁可不保护，也不能永久停写）。
    暂停的是 `WriteSample`（L0 落盘），采集、实时环、降采样、保留清理照常；恢复靠
    下一次检查自动完成。状态经 `meta.disk` 暴露给前端做提示，设置改动后调
    `Invalidate()` 立即重算。
24. **历史页下拉框 + 滚轮联动（v0.5.15）**：`<select>` 的 `:value` 必须始终是选项之一，
    所以 `loadWindow` 不能回写"实际跨度"，要回写吸附后的档位（对数距离取最近档）；
    滚轮缩放后只改档位显示、不重新取数（放大时已加载数据一定够用）；缩小到
    100% 还继续滚时抛 `zoombeyond`，由上层切到下一档并重取（分页式加载），否则
    用户永远只能缩到当前档位的全量。缩放比例改成按 `deltaY` 连续计算
    （`pow(1.0015, deltaY)`，钳制 0.5~2），触控板小步长也顺滑。
21. **独立页（温度/网络/磁盘）的窗口档位走两条数据路径**（v0.5.14）：`winSec <= 900`
    走 `/api/ring?sec=N`（实时环，1s 采样），更长的窗口走 `/api/series?level=l0`。
    后端 `ring` 对 `sec > 900` 直接钳成 900，所以前端必须自己按 900 分界，不能只把
    档位丢给后端。下拉框选项文案复用概览的 `win.*`（不是 `fmtDur`），这样两处显示
    完全一致。

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
- 0.5.x 移动端打磨（列宽拖动/触摸 tooltip/窄屏横向滚动）、历史导出导入、三语
- 0.5.9 功耗估算：概览卡片 + 历史曲线（本机无功率计，模型推算，标注 EST）
- 0.5.10 历史页滚轮缩放修复（补注册 dataZoom inside）、缩放换层 NaN/竞态/合成事件
- 0.5.11 滚轮缩放改为以鼠标位置为锚点（接管 wheel：convertFromPixel 反推 start/end）
- 0.5.12 历史页窗口档位按分组区分：温度/网络/磁盘与概览一致（10秒~24小时），其余保留长窗口
- 0.5.13 温度/网络/磁盘改为「概览细档 + 历史长档」合并 11 档（用户要再加而不是替换）
- 0.5.14 温度/网络/磁盘**独立页**窗口从 4 按钮改为下拉框 8 档（10秒~24小时，与概览同）
- 0.5.15 TCP 直连加 Basic 登录（浏览器弹窗）+ 磁盘空间保护暂停落盘 + 历史页下拉框与滚轮档位联动
