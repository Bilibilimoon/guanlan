# HTTP API 参考

观澜的全部接口都是 `/api/*` 下的 JSON。除 `/api/healthz` 外全部需要鉴权；
写操作还要求防跨站请求头。两条通道的鉴权矩阵见 [DEVELOPMENT.md](../DEVELOPMENT.md) §3。

- **fnOS 统一网关通道**（应用中心打开）：管理员由网关注入 `X-Trim-Isadmin: true`；
  普通用户一律 403。
- **TCP 直连通道**（`http://NAS-IP:9780`）：只放行 GET；在设置页启用「远程访问登录」
  后需 HTTP Basic 凭据，同一来源连续失败 5 次暂停 60 秒。
- **写操作**（POST/PUT）额外要求请求头 `X-Requested-With: guanlan`，否则 403（防跨站）。

错误统一为非 200 + `{"error":"<code>"}`，例如 `{"error":"admin_only"}`。

## 通用

| 端点 | 方法 | 鉴权 | 说明 |
|---|---|---|---|
| `/api/healthz` | GET | 无 | 存活探针，恒返回 `ok` |
| `/api/realtime` | GET | 读 | 最新一个采集样本 |
| `/api/ring?sec=N` | GET | 读 | 内存环形缓冲快照（N ≤ 900，>900 按钳制处理） |
| `/api/series` | GET | 读 | 历史时序查询（见下） |
| `/api/meta` | GET | 读 | 版本 / 网卡 / 磁盘 / 传感器清单 / 内外网 IP / 存储与磁盘守卫状态 |
| `/api/processes` | GET | 读 | 进程列表（只读快照） |
| `/api/alerts` | GET | 读 | 未读数 + 活跃告警 + 近期历史 + 当前规则 |
| `/api/config` | GET | 管理 | 当前配置（采集间隔、保留天数、登录开关、磁盘阈值） |
| `/api/config` | POST | 管理 + CSRF | 修改配置（见下） |
| `/api/config/export` | GET | 管理 | 导出配置 JSON |
| `/api/config/import` | POST | 管理 + CSRF | 导入配置 JSON |
| `/api/data/clear` | POST | 管理 + CSRF | 清空历史数据，body 必须为 `{"confirm":"CLEAR"}` |
| `/api/alerts/rules` | POST/PUT | 管理 + CSRF | 整体替换告警规则（≤32 条） |
| `/api/alerts/ack` | POST | 管理 + CSRF | 确认告警 |
| `/api/visitors?hours=24` | GET | 读 | 按来源 IP 汇总的访问记录（`hours` 1~720；可选 `ip` 子串、`fail=1`、`limit`） |

## 序列查询 `GET /api/series`

| 参数 | 说明 |
|---|---|
| `metrics` | 逗号分隔的序列名（≤24 个），见下表 |
| `start` / `end` | Unix 秒，`start < end`，否则 400 |
| `level` | `l0`（原始，默认保留 30 天）/ `l1`（5 分钟聚合，180 天）/ `l2`（1 小时聚合，365 天） |

响应：

```json
{
  "start": 1792974304, "end": 1792981306, "level": "l0",
  "series": {
    "cpu":        [[1792974304, 12.5], [1792974314, 9.1]],
    "temp.sda":   [[1792974304, 38.0], [1792974314, 38.0]]
  }
}
```

时间值为 Unix 秒，数组第二位为数值（百分比 0-100、°C、B/s、RPM、W）。

## 序列命名

| 前缀 / 名称 | 含义 |
|---|---|
| `cpu`、`mem_pct`、`swap_pct` | CPU 总占用、内存、Swap 占用（%） |
| `gpu_pct`、`npu_pct` | GPU/NPU 活动代理（实时频率÷最高频率，非官方利用率） |
| `load1` / `load5` / `load15` | 系统负载 |
| `power_w` | 整机功率**估算**（无功率计，模型推算，约 ±30%） |
| `fan` | 风扇转速（RPM） |
| `temp.<zone\|盘名>` | 温度（°C）：thermal zone 或磁盘 |
| `net.<iface>.rx` / `.tx` | 网卡速率（B/s） |
| `disk.<dev>.r` / `.w` | 磁盘读写速率（B/s） |
| `uptime` | 运行时长 |

## `POST /api/config`

body 为部分更新（只传要改的字段）：

```json
{
  "intervalMs": 2000,
  "dbIntervalMs": 10000,
  "retainDays": { "l0": 30, "l1": 180, "l2": 365 },
  "authEnabled": true,
  "authUser": "admin",
  "authPass": "at-least-4-chars",
  "diskMinGb": 5
}
```

约束：`intervalMs` 100~15000；`dbIntervalMs` 只接受 5000/10000/15000/30000/60000；
保留天数 1~3650 且 `l0 ≥ 1`；`authPass` 4~128 字符（启用登录时必填，密码不回显）；
`diskMinGb` 0~100000（0 = 关闭保护）。响应回显修改后的完整配置。

## 隐私与安全说明

- `/api/healthz` 固定免认证（供网关/监控探活），只返回 `ok`，不泄漏任何信息。
- 密码只以加盐迭代摘要存于本地 SQLite，接口任何响应都不含明文或摘要。
- TCP 通道入口会剥掉全部 `X-Trim-*` 请求头，伪造管理员身份无效（有单测覆盖）。
