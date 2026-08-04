# AGENTS.md

本文档为 AI Agent (如 Codex, Antigravity, Claude 等) 在本仓库协作开发时提供指导与约束规范。

## 1. 项目概述

`switch-codex` 是一个基于 **Tauri v2** 构建的 macOS 优先的桌面应用程序，用于管理和快速切换多个 Codex `auth.json` 账号配置。

### 技术栈
- **后端 / 核心逻辑**: Rust (Tauri v2) (`src-tauri/`)
- **前端 UI**: Vanilla HTML / CSS / JavaScript (原生前端，无框架依赖) (`src/`)
- **包管理器 / 运行环境**: Node.js >= 22 (配置见 `.nvmrc` 和 `package.json`)，包管理器为 `npm`

---

## 2. 项目目录结构

```text
switch-codex/
├── src/                    # 前端代码目录
│   ├── index.html          # 主界面 HTML 结构
│   ├── renderer.js         # 前端交互与 Tauri IPC 命令调用逻辑
│   └── styles.css          # UI 样式
├── src-tauri/              # Rust 后端目录
│   ├── src/
│   │   ├── main.rs         # 应用入口、菜单/ macOS Status Item (托盘) 逻辑及 IPC 接口
│   │   └── store.rs        # 账号数据存储、auth.json 原子替换与校验逻辑
│   ├── Cargo.toml          # Rust 项目依赖
│   └── tauri.conf.json     # Tauri v2 配置文件
├── data/                   # 默认数据存储目录
│   ├── accounts.json       # 账号配置列表元数据
│   └── accounts/           # 存储各账号对应的 auth.json
├── .github/workflows/      # CI/CD 工作流
│   ├── lint.yml            # 代码校验
│   └── release.yml         # 自动构建并发布 release
└── package.json            # Node 项目脚本配置
```

---

## 3. 开发与构建指令

### 环境准备
```bash
nvm use
npm install
```

### 开发调试
```bash
# 启动 Tauri 开发开发模式
npm run dev
```

### 代码检查 (Lint & Check)
修改代码后，必须运行对应的检查命令确保代码无错误：

```bash
# 前端 JS 语法检查
npm run lint

# Rust 后端类型与编译检查 (在 src-tauri 目录下执行)
cd src-tauri && cargo check
```

### 跨平台打包构建 (Build)
```bash
# macOS Apple Silicon (arm64)
npm run build:mac:arm

# macOS Intel (x64)
npm run build:mac:x64

# Windows (x64)
npm run build:win:x64
```
构建产物输出至 `src-tauri/target/<target>/release/bundle/`。

---

## 4. 数据与核心业务逻辑

- **全局数据目录重定向**: 默认存储在 `data/`，可通过环境变量 `CODEX_SWITCH_DATA_DIR` 进行覆盖。
- **配置切换与安全保障**:
  - 当前激活的 Codex 认证文件存储在 `~/.codex/auth.json`。
  - 在切换账号时，核心逻辑会校验目标 `auth.json`，并保留备份 `~/.codex/auth.json.switch-codex.bak` 以防凭证丢失。
  - 文件替换必须保证原子性与可靠性。

---

## 5. AI Agent 协作规范

1. **保持轻量性**: 前端采用 Vanilla JS/CSS 设计，除非用户明确要求，否则请勿随意引入大型前端框架（如 React/Vue）或无关的 npm 包。
2. **每次修改后进行代码验证**:
   - 修改前端 `renderer.js` 后，执行 `npm run lint`。
   - 修改 Rust 代码后，进入 `src-tauri` 目录执行 `cargo check` 或 `cargo clippy`。
3. **跨平台兼容意识**: 本项目支持 macOS (arm64/x64) 与 Windows (x64) 构建。虽然当前为 macOS 优先（支持应用菜单和状态栏 Status Item），但底层 Rust 核心功能需保持平台无关性。
4. **注释与文档保持**: 修改代码时保留现有清晰的注释说明，涉及底层协议或存储格式变动时同步更新 README.md 及相关文档。
