# ModuleBalancing

`ModuleBalancing` 是一个用于模块文件集中管理的 Go 服务，面向“服务端模块仓库 + 客户端本地缓存 + 备份节点”这一场景，负责模块分发、AOD 解析、过期清理、客户端存储记录、客户端升级分发以及备份节点间回源。

项目当前基于以下组件构建：

- Go
- Gin
- gRPC
- GORM
- MySQL
- fsnotify / notify

## 核心能力

- 统一提供 HTTP 与 gRPC 服务，并复用同一监听端口
- 管理服务端本地模块目录，并在启动时自动同步到数据库
- 实时监听模块目录新增文件，自动计算 CRC64、文件大小和过期时间后入库
- 提供模块下载、断点续传、完整性校验和元数据查询能力
- 当本地模块缺失时，可自动从备份节点拉回并恢复数据库记录
- 定时扫描服务端过期模块，上传到备份节点后删除本地文件与数据库记录
- 接收客户端上报的模块使用记录，维护客户端模块过期时间
- 主动向在线客户端推送已过期模块，通知客户端删除
- 提供客户端升级文件的 MD5 检查与分块下发能力
- 提供基础的 HTTP 统计接口，便于前端页面或运维侧查看在线客户端和过期分布

## 典型业务流程

### 1. 服务启动

启动时会按顺序完成以下动作：

1. 读取 `conf/config.yaml`
2. 初始化业务日志目录
3. 连接 MySQL，并执行 GORM 自动迁移
4. 读取 `temp/modulebalancingclient.exe`，作为客户端升级源文件
5. 扫描 `Setting.Common` 指向的模块目录，将数据库中不存在的本地文件补录入库
6. 启动以下后台任务：
   - 配置热加载
   - 模块目录监听
   - 服务端模块过期检查
   - 客户端模块过期检查
   - 无效数据库记录清理
   - 客户端升级文件监控
7. 启动 gRPC + Gin 多路复用服务

### 2. 模块下载

客户端调用 `Module.Push` 下载模块时：

1. 服务端先检查数据库记录和本地文件
2. 若数据库存在但文件丢失，则尝试从备份节点回源
3. 若数据库和本地都不存在，则返回 `NotFound`
4. 若本地存在但数据库无记录，则重新计算 CRC64 并补录数据库
5. 服务端通过 gRPC stream 分块发送文件，并在 header 中附带：
   - 文件名
   - 文件大小
   - CRC64
   - 创建时间
   - 修改时间
6. 下载成功后，累计客户端下载量

### 3. AOD 解析

客户端调用 `Module.Analyzing` 上传 AOD 内容后：

1. 服务端先从 AOD 中提取 `.CRI` 文件名
2. 再解析 `.CRI` 文件中的 `ModuleName` 列表
3. 若所需 `.CRI` 本地不存在，会尝试从备份节点下载
4. 最终返回：
   - 解析出的模块名列表
   - 解析失败或缺失的文件列表

### 4. 客户端存储记录与过期回收

客户端通过 `Storerecord.Updatestorerecord` 上报自身正在使用的模块后：

- 服务端会更新对应模块在服务端的 `lastuse` 和服务端保留期
- 同时为客户端写入 `clientmodules` 记录，并按客户端的保留天数计算过期时间
- 后台定时任务会检查客户端已过期模块
- 若客户端在线，则通过 `Expirationpush.Expiration` 流式推送删除通知

### 5. 服务端模块过期处理

后台定时扫描 `modules` 表中过期记录：

1. 逐个询问备份节点是否允许存放
2. 分块上传模块文件到备份节点
3. 上传成功后删除本地文件
4. 同步删除数据库记录

## 项目结构

```text
.
├── Modulebalancing.go       # 程序入口，初始化配置/数据库/日志/HTTP/gRPC/后台任务
├── api/                     # gRPC 服务实现
├── clientcontrol/           # 客户端升级文件管理
├── conf/                    # 配置文件
├── db/                      # GORM 模型与自动迁移
├── env/                     # 配置结构、文件监控、CRC、回源/备份等通用能力
├── grpc/                    # proto 与生成代码
├── logmanager/              # 业务日志封装
├── route/                   # HTTP 路由
├── temp/                    # 运行时临时文件、客户端升级文件
├── web/                     # 前端页面资源
└── logs/                    # 运行日志输出目录
```

## 数据模型

当前项目主要维护 4 张业务表：

- `modules`
  - 服务端本地模块记录
  - 字段包含 `name`、`crc64`、`size`、`lastuse`、`expiration`
- `clients`
  - 客户端节点信息
  - 字段包含 `serveraddress`、`maxretentiondays`、`status`、`accumulate_download`、`reload`
- `clientmodules`
  - 客户端已存储模块记录
  - 字段包含 `partnumber`、`name`、`expiration`
- `normalmodules`
  - 常驻/共享模块记录
  - 用于避免误删客户端共享模块

## 配置说明

配置文件路径：`conf/config.yaml`

示例：

```yaml
Setting:
  Expiration: 180
  CheckUnwanted: 500
  CheckExpiration: 10
  CheckClientExpiration: 10
  Common: D:\Common

Database:
  Host: localhost
  Port: 3306
  Username: root
  Password: your-password

GRPC:
  Port: 9998

Backup:
  - Host: localhost
    Port: 9999
```

字段说明：

- `Setting.Expiration`
  - 服务端模块默认保留天数
- `Setting.CheckUnwanted`
  - 检查“数据库有记录但文件已不存在”的频率，单位分钟
- `Setting.CheckExpiration`
  - 检查服务端模块是否过期的频率，单位分钟
- `Setting.CheckClientExpiration`
  - 检查客户端模块是否过期的频率，单位分钟
- `Setting.Common`
  - 服务端模块存放目录
- `Database.*`
  - MySQL 连接参数
- `GRPC.Port`
  - 服务监听端口
- `Backup`
  - 备份节点列表；本地缺失模块时会尝试从这里回源，服务端过期模块也会上传到这些节点

## 运行要求

### 1. 环境准备

- Go 版本需满足 `go.mod` 要求
- MySQL 可用，并存在 `modulebalancing` 数据库
- `Setting.Common` 指向的目录可读写
- `temp/modulebalancingclient.exe` 文件存在，否则程序初始化会失败

### 2. 启动项目

在项目根目录执行：

```powershell
go run .
```

构建可执行文件：

```powershell
go build -o ModuleBalancing.exe .
```

在受限环境下建议把 Go 缓存目录放到工作区：

```powershell
$env:GOCACHE="$PWD/.gocache"
$env:GOMODCACHE="$PWD/.gomodcache"
go build ./...
go test ./...
```

## HTTP 接口

服务会在同一端口上同时提供 HTTP 和 gRPC。HTTP 主要用于展示统计信息。

基础页面：

- `GET /index`
  - 返回 `web/index.html`

统计接口前缀：`/collect`

- `GET /collect/serverstart`
  - 返回服务启动时间
- `GET /collect/clientonlinelist`
  - 返回客户端列表、状态、累计下载量、分组、保留天数
- `GET /collect/clientexpirationlist/:device`
  - 统计当月每天的即将过期容量
  - `device=client` 表示客户端维度
  - `device=server` 表示服务端维度
- `GET /collect/clientmodulelist/:address`
  - 查询指定客户端当前记录的模块清单
- `GET /collect/clientsexpirationaccumulated`
  - 查询所有客户端的模块过期明细，并按过期时间排序

返回格式统一为：

```json
{
  "status": "ok",
  "result": {}
}
```

## gRPC 接口

Proto 文件：`grpc/ModuleBalancing.proto`

### Module 服务

- `Upload(stream UploadRequest) returns (UploadResponse)`
  - 备份节点接收模块文件上传
- `Analyzing(analyzingRequest) returns (analyzingResponse)`
  - 解析 AOD / CRI 内容，返回模块名列表和失败项
- `IntegrityVerification(IntegrityVerificationRequest) returns (IntegrityVerificationResponse)`
  - 返回指定模块的文件名、大小、CRC64
- `Push(ModuleDownloadRequest) returns (stream ModulePushResponse)`
  - 下载模块文件，支持 offset 续传
- `ModuleReload(ModuleReloadRequest) returns (EmptyResponse)`
  - 重新计算本地文件 CRC64 和大小，并更新数据库
- `AllowStorage(AllowStorageRequest) returns (AllowStorageResponse)`
  - 询问备份节点当前是否允许接收指定大小文件

### Expirationpush 服务

- `Expiration(ExpirationPushRequest) returns (stream ExpirationPushResponse)`
  - 客户端建立长连接
  - 服务端持续推送心跳与过期删除消息

### Storerecord 服务

- `Updatestorerecord(stream StorerecordRequest) returns (StorerecordResponse)`
  - 客户端上报模块使用记录
  - 服务端同步更新客户端缓存记录和服务端模块保留时间

### ClientCheck 服务

- `MD5(MD5Request) returns (MD5Response)`
  - 返回客户端升级包 MD5
- `Data(DataRequest) returns (stream DataResponse)`
  - 返回客户端升级包二进制内容

## 日志

程序启动时会为多个业务域创建独立日志目录，主要包括：

- `logs/db`
- `logs/clientstore`
- `logs/expiration`
- `logs/dl`
- `logs/monitor`
- `logs/upload`
- `logs/dump`
- `logs/expirationforclient`
- `logs/integrityverification`
- `logs/analyzing`
- `logs/unwanted`
- `logs/reload`

这意味着排查问题时可以按业务链路定位日志，而不是翻统一大日志文件。

## 开发说明

- 修改 Go 代码后，提交前执行：

```powershell
gofmt -w Modulebalancing.go api\*.go db\*.go env\*.go route\*.go clientcontrol\*.go
```

- 当前仓库基本没有测试文件，新增功能建议补充对应包的 `*_test.go`
- 若修改 `grpc/ModuleBalancing.proto`，应同步重新生成 `grpc/*.pb.go`

## 已知限制

- 当前配置文件中可能包含本地数据库账号或机器路径，部署前应改为实际环境配置
- 项目明显偏向 Windows 运行环境，部分文件时间处理和路径处理逻辑使用了 Windows API
- 客户端升级文件依赖固定路径 `temp/modulebalancingclient.exe`
- HTTP 与 gRPC 共用同一端口，接入层需允许 HTTP/2 / h2c

## 适用场景

这个项目适合以下场景：

- 内网模块文件分发平台
- 客户端本地模块缓存管理
- 带保留期的模块仓库
- 多节点备份与自动回源
- 需要同时维护“服务端文件生命周期”和“客户端文件生命周期”的系统
