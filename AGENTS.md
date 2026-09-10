# AGENTS.md

本文档为 AI Agent 在本仓库协作开发时提供指导与约束。

## 1. 项目概述

`switch-codex` 是一个基于 **Wails v3 Beta** 的跨平台桌面应用，用于管理和快速切换多个 Codex `auth.json` 账号，并提供订阅额度、本地用量统计和每日定时调用。

### 技术栈

- **后端 / 核心逻辑**：Go 1.26（`main.go`、`app.go`、`internal/`）
- **桌面框架**：Wails `v3.0.0-beta.18`，Go 依赖、CLI 和 `@wailsio/runtime` 必须固定为相同版本
- **前端 UI**：TypeScript + React + Ant Design + AntV（`src/`）
- **前端构建**：Vite
- **包管理器**：Node.js >= 22.12，npm

## 2. 主要目录

```text
switch-codex/
├── main.go                  # Wails 入口、窗口、系统事件
├── app.go                   # 前端绑定服务
├── menus.go                 # 应用菜单和 macOS 托盘菜单
├── internal/
│   ├── platform/            # 原子替换、文件锁、数据目录和平台差异
│   ├── store/               # 账号存储与凭证切换
│   ├── usage/               # 配额、本地统计和价格目录
│   └── scheduler/           # 每日调度与进程树清理
├── src/                     # 现有 React 前端和 Wails 桥接层
├── build/                   # Wails、macOS、Windows、Linux 构建元数据
├── scripts/                 # 版本同步和跨平台构建脚本
├── data/                    # 开发模式本地数据（凭证被 Git 忽略）
└── .github/workflows/       # 检查与四平台发布
```

`src-tauri/` 已从版本控制移除。开发机上若还存在该目录，它只用于旧数据迁移，整个目录必须保持 Git 忽略。

## 3. 开发和验证

```bash
nvm use
npm install
npm run desktop:setup
npm run dev
```

修改后按影响范围运行检查；提交前必须全部通过：

```bash
npm run lint
npm test
npm run build:web
npm run bindings:check
go test ./...
go vet ./...
go test -race ./internal/...
```

修改任何绑定服务方法或 DTO 后，运行 `npm run bindings` 并提交 `src/bindings/` 的更新。不要手工修改生成的绑定。

## 4. 跨平台构建

```bash
npm run build:mac:arm
npm run build:mac:x64
npm run build:win:x64
npm run build:linux:x64
```

安装包输出到 `release/`：macOS 为 DMG，Windows 为 NSIS EXE，Linux 为 DEB。中间产物在 `bin/`。macOS 最低版本为 12，支持 arm64/x64；Windows 支持 x64 和 Windows 10 以上版本；Linux 支持 amd64 架构的 Ubuntu 24.04 和 Debian 13 以上版本。Linux 包必须在 Ubuntu 24.04 上使用 GTK4 与 WebKitGTK 6.0 原生构建。正常开发和构建不得依赖 Rust 工具链。

## 5. 数据和安全边界

- `CODEX_SWITCH_DATA_DIR` 优先于所有默认路径。
- 开发版使用项目 `data/`；正式版沿用 `com.switchcodex.app/data` 的旧版目录。
- 当前激活文件为 `~/.codex/auth.json`；切换前保留 `auth.json.switch-codex.bak`。
- 凭证文件必须使用同目录临时文件、刷盘和原子替换；Unix 权限保持 `0600`。
- 配额和调度凭证只在 Go 后端处理。定时调用必须使用独立临时 `CODEX_HOME`，退出或超时时清理整个进程树。
- `scheduler.lock` 必须继续阻止新旧应用同时操作同一数据目录。
- 不得提交 `data/` 或遗留 `src-tauri/data/` 中的账号、设置、缓存、锁和临时凭证。

## 6. 协作和发布规则

1. 优先复用现有 React、Ant Design 和 AntV 依赖，避免无关 npm 包或 UI 改版。
2. 业务包不得依赖 Wails 窗口对象；通过服务层和接口注入时钟、HTTP、进程执行器及事件出口。
3. 保持 macOS、Windows 和 Linux 差异明确；修改文件操作、调度或进程代码时必须考虑三个平台。
4. 数据格式、命令或事件契约变化时同步更新测试和 README。
5. `package.json` 是唯一版本来源。更新版本后运行 `npm run version:sync`；`npm run lint` 会检查生成元数据、Wails 三方版本和 Git Tag。
6. 发布版本必须是尚未发布的 `MAJOR.MINOR.PATCH`。已有 Tag 只允许在该 Tag 指向当前提交的正式构建中复用。
