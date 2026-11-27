# Branchline 架构与异步边界

## 分层

| 层 | 位置 | 职责 |
| --- | --- | --- |
| 入口 | `main.go` | 仅调用 `app.Run()`，不含业务逻辑 |
| 应用壳与视图 | `internal/app` | 单窗口布局、路由、调色板、控件与各功能页；只读 UI 状态，不在绘制中执行 Git |
| 仓库会话 | `internal/app/session.go` | 持有当前仓库 `*git.Repo`（本机或 SSH）、快照、generation 与统一写入口 |
| 快照编排 | `internal/repository` | 使用捕获的只读选项并发组装 `Snapshot`，不依赖窗口或 UI 状态 |
| Git 与领域包 | `internal/git`、`internal/store`、`internal/sshtest` 等 | Git 命令与本机/SSH 执行器、持久化、SSH 测试底座等，不依赖 MyGo 界面 |

`internal/app` 内部按模块分文件：`app.go`、`ui_main.go`、`ui_kit.go`、`session.go`、`ui_diff.go` 为共享壳层与协议；`ui_history.go`、`ui_changes.go`、`ui_refs.go`、`ui_repos.go`、`ui_sync.go` 分别承载各核心功能页并各自在文件内扩展其挂载在 `app` 上的页面状态。

功能的界面状态用 `featureState` 按类型挂在 `app` 上，不再新建包级的 `map[*app]` 表：条目永不删除会留下整个测试进程的状态，锁也掩盖了访问线程的问题。这些状态与 `app` 的其他字段一样只在 UI 线程读写，worker 不访问；新建功能的状态同样走 `featureState`，不改 `app.go`。

## 后台操作约束

1. **UI 线程捕获参数**：启动后台读或写前，必须在 UI 线程捕获 `*git.Repo`、`repository.Options`、提交信息、路径和目标引用。worker 不读取 `app`、`repoSession`、对话框草稿或 `store` 的可变字段。
2. **结果回到 UI 线程**：慢 I/O 与 Git 命令均在后台执行，结果通过 `Window.Update`（`app.post`）排队回到 UI 线程。`post` 是异步入队，不是同步调用；禁止在 `post` 闭包写入后于后台线程立即读取返回值。
3. **generation 丢弃过期结果**：
   - `repoGen` 在每次切换或关闭仓库时递增；旧仓库的快照读、写完成或局部读回到 UI 线程时必须直接丢弃，不得改写新仓库的快照、loading、busy 或错误。
   - `loadGen` 在每次重读或开始写操作时递增；被取代的旧读取不得覆盖新快照，也不得清除新请求的 loading 或错误。
   - 局部异步读（如提交详情、文件 diff）走 `sessionRead`，同时校验 `repoGen` 与作用域序号。
4. **统一写入口**：修改仓库状态的 Git 操作统一经 `repoSession.do` / `doThen` 执行：
   - 设置忙状态并拒绝重入（忙时返回 `false`，不启动第二个 worker）；
   - 开始前推进 `loadGen`，避免写前旧读覆盖写后状态；
   - 完成回到 UI 线程且 `repoGen` 仍一致时清除忙状态，无论成功或失败均安排重读快照，并将写错误保留在独立的 `actionErr`（不与快照加载错误 `loadErr` 互相覆盖）；
   - `doThen` 的 `after` 回调在安排重读之后执行，不得假设重读已完成。
5. **不因绘制触发请求**：视图单次事件可能重建多次，禁止在 `view` 中无条件发起 Git 或网络请求；失败状态保留至用户显式重试或切换仓库。
6. **MyGo 生命周期**：不保存 `*ui.Context`、`ui.Element` 或部件值到状态或 goroutine；使用 `c.Key(...)` 与 `ui.Handle` 保持控件身份，由 `go tool mygo vet .` 守护。
7. **确定性测试调度**：界面测试使用 `internal/app/helpers_test.go` 的 `uiQueue` 显式排空 worker 回调，并用 `PauseWorkers` 控制并发顺序，不用 `time.Sleep` 轮询，也不用 `go fn()` 代替生产调度。

## MyGo 可访问性与测试已知坑

以下行为已在当前依赖的上游 `github.com/egoist/mygo v0.10.0` 源码中确认，编写界面与 `ui.Tester` 测试时必须遵守（升级 mygo 版本时复核）：

1. **裸 `ui.Row` 不会把 `AccessSelected` 映射成 `Tester` 的 `Selected`**：
   - 原因：裸 `ui.Row` 的默认可访问性角色是 `RoleGroup`（`ui/access.go:616-619`）；`AccessSelected` 底层走的是 `Checked`（`ui/access.go:460`），而 `ui.Tester` 仅在 `RoleTab`、`RoleTreeItem`、`RoleRow`、`RoleListItem`、`RoleCell`、`RoleColumnHeader`、`RoleRowHeader` 这 7 种角色上才把 `Checked` 映射成 `AccessInfo.Selected`（`ui/tester_access.go:239-243`）。
   - 正确写法：自定义可选列表行必须显式设置 `Role(ui.RoleListItem)`（或其他上述受支持的角色）：
     ```go
     ui.Row(c.Key(id)).Role(ui.RoleListItem).AccessSelected(selected)
     ```
2. **`.Label()` 非空时会替换节点原文本，改变 `Tester` 匹配目标**：
   - 原因：元素的 `.Label()` 非空时，运行时用 `label` 替换其自身的 `e.text` 写入 `rt.labels` 并进入可访问性树（`ui/runtime.go:1815-1825`）；此后 `ui.Tester` 的 `Find`、`Texts`、`HasText`、`Click` 全部按该 `label` 匹配，不再按元素的原显示文本匹配。
   - 正确写法：需要为列表行或按钮指定唯一可访问性标签时，把 `.Label(...)` 加在外层交互容器上，保留内层需要被文本断言的 `ui.Text` 不设 `.Label()`（测试中点击该容器时按显式 `Label` 定位）：
     ```go
     ui.Row(c.Key(id)).Role(ui.RoleListItem).Label("Commit " + shortHash).Children(func() { ui.Text(c, subject) })
     ```
