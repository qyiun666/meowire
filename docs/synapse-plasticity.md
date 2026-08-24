# Synapse 可塑契约设计（1.1.1）

> 目标：把 `Synapse` 从"二方法静态连接"升级为**可塑突触图**，作为仿生人体架构基座的一部分。
> 框架只提供契约与状态存储（权重、计数）；学习规则（赫布 / STDP）由宿主实现（1.1.2 提供参考实现）。

## 1. 仿生对应

| 生物机制 | 接口操作 | 语义 |
|---|---|---|
| 突触发生（synaptogenesis） | `Link(from, to, weight)` | 建立连接并设初始强度 |
| 突触消除 | `Unlink(from, to)` | 断开连接 |
| 长时程增强/抑制（LTP/LTD） | `Reinforce(from, to, delta)` | 权重上调 / 下调（delta 可正可负） |
| 突触强度与活动统计 | `Edges(from)` | 查询边（权重 + 累计传递计数） |
| 信号传递 | `Fire(sig)` | 沿已建立连接投递（语义不变） |
| 修剪（pruning） | `Edges` + `Unlink` 组合 | 宿主按权重阈值清理弱连接（框架不内置策略） |

## 2. 设计原则

- **框架存状态，不决策**：权重、传递计数由框架存储；何时增强 / 衰减 / 修剪由宿主决定
- **单路径、无兼容 shim**：`Synapse` 接口直接升级，旧二方法实现清理（不保留两套接口）
- **学习规则宿主实现**：赫布 / STDP 在宿主侧，框架只提供原语（`Reinforce` / `Edges` / `Unlink`）

## 3. 接口契约

```go
// Edge 是突触图的边：一条 from→to 的连接及其强度。
type Edge struct {
	From   string
	To     string
	Weight float64 // 突触强度（宿主学习规则读写）
	Fired  int64   // 累计成功传递次数（宿主统计用）
}

// Synapse 是跨 Agent 连接契约（1.1.1 起为可塑突触图）。
type Synapse interface {
	// Link 建立或更新突触：from→to，初始强度 weight。
	// 已存在则覆盖权重（幂等，不报错）。
	Link(ctx context.Context, from, to string, weight float64) error
	// Unlink 断开突触。连接不存在返回 ErrNotLinked。
	Unlink(ctx context.Context, from, to string) error
	// Reinforce 调节权重：delta 正 = 增强（LTP），负 = 衰减（LTD）。
	// 结果权重不低于 0（clamp）；连接不存在返回 ErrNotLinked。
	Reinforce(ctx context.Context, from, to string, delta float64) error
	// Fire 沿已建立连接投递信号（语义不变）；成功投递累计 Fired。
	Fire(ctx context.Context, sig nerve.Signal) error
	// Edges 返回 from 的所有出边快照；from == "" 返回全图边。
	// 全图快照即持久化导出原语（宿主序列化存盘）。
	Edges(ctx context.Context, from string) ([]Edge, error)
}
```

## 4. Direct 升级要点

- 存储：`map[string]map[string]Edge`（from → to → Edge），现有 `RWMutex` 保护
- `Link`：已存在则覆盖权重（不报错）；`Unlink` / `Reinforce`：连接不存在返回现有 `ErrNotLinked`
- `Fire`：投递成功路径对目标边 `Fired++`（写锁内完成）
- `Edges`：返回深拷贝快照（调用方修改不影响内部状态）
- `Link` / `Reinforce` 结果权重为负：clamp 到 0（权重下限 0）

## 5. 状态持久化与恢复（快照往返）

突触图的**存储介质**（文件 / DB）与**序列化格式**（JSON 等）由宿主自选——框架零依赖、不消费，只提供两个原语：

```go
// ① 导出（运行中随时可做）：全图快照 → 宿主序列化存盘
edges, _ := synapse.Edges(ctx, "") // []Edge{From, To, Weight, Fired}

// ② 恢复（下次启动装配时）：宿主读盘反序列化 → 组合根注入初始边
restored, _ := loadEdges() // []Edge
synapse := synapse.NewDirect(resolver, restored...) // 空图：NewDirect(resolver)
```

- **Agent.New 不感知连接状态**：Synapse 是宿主域装配件（宿主自己的 send_message 工具依赖），不是 Agent 六端口；恢复在组合根完成，`Agent.New` 签名不变
- `NewDirect(r Resolver, initial ...Edge)`：可变参数可选初始边——不传 = 空图（现有调用零改动），传 = 恢复图
- 生命周期：`存盘(Edges 全图) → 进程重启 → 注入(NewDirect initial) → 继续演化(Reinforce/Unlink) → 再存盘`

## 6. 宿主学习循环模式（1.1.2 已提供参考实现）

```go
// ① 赫布规则：一起发放的连接加强（直接使用参考实现 Hebbian / HebbianFire）
if sig 投递成功 {
	synapse.Hebbian(ctx, s, from, to, 0.1)
}

// ② STDP 时序窗（pre/post 发放时间差决定增强/衰减）
synapse.STDP(ctx, s, from, to, dt, synapse.STDPParams{APlus: 0.5, AMinus: 0.4, Tau: 20 * time.Millisecond})

// ③ 周期修剪：扫描弱边（参考实现 Prune：权重低于阈值且低频 → 剪除）
removed, _ := synapse.Prune(ctx, s, 0.3, 10)
```

参考实现位于 `internal/synapse/learning.go`（`Hebbian` / `STDP` / `Prune` / `HebbianFire`），宿主可直接调用或参考改造；框架从不自动应用学习规则。

## 7. 迁移指南（宿主）

1. `Link` 调用补第三个参数：初始强度（如 `1.0`）
2. 自定义 `Synapse` 实现补 `Unlink` / `Reinforce` / `Edges` 三方法
3. 使用框架 `Direct` 实现的宿主：无需迁移（随框架升级）；`NewDirect(resolver)` 空图形态不变，可选择性传初始边恢复
4. `Fire` 语义不变；基于 `Fire` 的宿主工具（send_message）无影响
5. 持久化接入：宿主在组合根加两步（`Edges(ctx, "")` 存盘；启动时 `NewDirect(resolver, restored...)`）

## 8. 测试计划

- `Link` 幂等覆盖权重；`Unlink` / `Reinforce` 不存在返回 `ErrNotLinked`
- `Reinforce` 负 delta 衰减、clamp 到 0 不出现负权重
- `Fire` 成功后 `Fired` 递增；`Edges` 快照隔离（外部修改不影响内部）
- 快照往返：`Edges(全图) → NewDirect(initial) → Edges(全图)` 逐边等价（含 Weight/Fired）
- 并发：多 goroutine `Link`/`Reinforce`/`Edges` 混合 -race 全过
- 旧二方法实现编译失败（期望的 breaking，单路径政策）

## 9. 版本边界

- **1.1.0**（已交付）：静态图闭环 + 审计 + 动态接线 + AgentCard
- **1.1.1**（已交付）：Synapse 可塑契约 + Direct 升级 + 测试 + 文档
- **1.1.2**（已交付）：赫布 / STDP 参考实现（learning.go：Hebbian/STDP/Prune/HebbianFire）+ 合成渲染（api/composite.go：BuildComposite/RenderComposite/RenderCompositeJSON——内部插槽子图 + 外部突触边一张图）+ 门面补全（NewDirect/Resolver 重导出）
