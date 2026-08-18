## Purpose

Session 持久化存储能力：将每个 torrent 的 resume 状态（下载进度、配置与统计）可靠地保存在 session 数据库文件中，支持启动恢复、自动迁移旧格式与随下载生命周期更新。

## ADDED Requirements

### Requirement: Resume 状态持久化

系统 SHALL 将每个 torrent 的 resume 状态持久化到 session 数据库。持久化的字段集合包括：base path、已完成的 piece bitfield、tags、custom 键值、tracker 列表、selected files、file paths、下载/上传速度限制、添加时间、完成时间、已下载/已上传/损坏字节数、tracker key、状态（stopped/active）、piece pick strategy、queue weight。

#### Scenario: 状态更新后写入

- **WHEN** 任一 resume 状态字段发生变化（如 bitfield 更新、标签修改、速度限制调整、统计变化）
- **THEN** 数据库中的对应记录被原子更新，重启后读取到最新值

#### Scenario: 字段语义保持

- **WHEN** 一次写入包含完整的 resume 字段集合
- **THEN** 读取时所有字段语义与持久化前一致（数组与映射内容完整、数值无精度损失、bitfield 位图无损）

### Requirement: 启动加载

系统 SHALL 在启动时从 session 数据库加载所有已保存 torrent 的 resume 状态，用于恢复下载。

#### Scenario: 启动恢复全部下载

- **WHEN** session 数据库中存在多条 torrent 记录
- **THEN** 全部记录被加载并恢复为对应的下载状态（stopped 或 active）

#### Scenario: 数据库缺失

- **WHEN** 启动时 session 数据库文件不存在
- **THEN** 创建新的空数据库，系统正常启动，不报错

### Requirement: 旧格式自动迁移

系统 SHALL 在启动时检测旧格式 `.resume` 文件，将其导入 session 数据库，导入成功后清理旧文件。

#### Scenario: 迁移全部成功

- **WHEN** 启动时存在多个旧 `.resume` 文件且均可解析
- **THEN** 所有状态导入数据库，旧文件被删除，下载按原状态恢复

#### Scenario: 单个文件损坏

- **WHEN** 启动时存在损坏或无法解析的旧 `.resume` 文件
- **THEN** 该文件被跳过并记录警告日志，其余文件正常迁移，启动不中断

#### Scenario: 无旧文件

- **WHEN** 启动时不存在任何旧 `.resume` 文件
- **THEN** 跳过迁移，直接从数据库加载

#### Scenario: 迁移后重复启动

- **WHEN** 迁移完成后再次启动
- **THEN** 不再触发迁移，直接加载数据库

### Requirement: 记录删除

系统 SHALL 在 torrent 被移除时删除数据库中的对应记录。

#### Scenario: 移除 torrent

- **WHEN** 用户移除一个 torrent
- **THEN** 该 torrent 的 resume 记录从数据库删除，重启后不再出现

### Requirement: 写入原子性

系统 SHALL 保证 resume 状态写入的原子性，进程崩溃不损坏已持久化的数据。

#### Scenario: 写入中断

- **WHEN** 进程在状态写入过程中崩溃
- **THEN** 数据库保持之前一次完整写入的状态，不出现部分写入或损坏，重启后系统可正常加载

#### Scenario: 并发读写

- **WHEN** 多个下载同时更新各自状态且启动加载并发执行
- **THEN** 各记录互不干扰，读取不阻塞在未完成写入上
