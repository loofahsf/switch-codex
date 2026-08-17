# Switch Codex

Switch Codex 是一款基于 Tauri 构建、以 macOS 优先的多 Codex `auth.json` 账号配置管理与快速切换桌面应用。

前端使用 TypeScript、React、Ant Design、AntV 和 Vite，核心账号与文件操作由 Rust/Tauri 后端完成。

## 功能特性

- 支持配置多个 Codex 账号名称。
- 将每个账号关联至项目本地存储的 `auth.json` 文件（存储于 `data/accounts/<account-id>/auth.json`）。
- 将指定账号标记为当前激活账号，并原子化替换 `~/.codex/auth.json`。
- 支持通过应用窗口、macOS 应用菜单或 macOS 菜单栏状态栏图标（Status Item）快捷切换账号。
- 校验导入的凭证文件，并在切换时保留前一次激活文件的备份 `~/.codex/auth.json.switch-codex.bak`。
- 查询各已保存 ChatGPT Codex 账号当前的订阅用量窗口（Usage Windows）。
- 汇总本地 `~/.codex/sessions` 中的输入 Token、缓存输入 Token、缓存写入 Token、输出 Token 及推理 Token 数量。
- 根据从 [OpenAI 官方价格页面](https://developers.openai.com/api/docs/pricing) 自动刷新的价格，估算等效 API 的美元费用。

## 用量统计

打开 **用量统计** 标签页即可查看：

- 每个已保存账号独立的短窗口与周度配额百分比。
- 按模型和日期分类汇总的本地 Token 总量。
- 基于 OpenAI 标准按 Token 计价估算的等效 API 成本。

用量配额查询使用与官方 Codex 客户端相同的只读 ChatGPT Codex 用量接口及账号 Header。凭证信息仅在 Rust 后端读取，绝不会返回给渲染进程或写入日志。Session 日志文件仅在本地解析，仅提取并保存时间戳、模型名称和 Token 计数器。

由于 ChatGPT/Codex 付费计划属于订阅制产品，界面展示的美元金额为对比参考估算值，而非实际账单。未公布价格的模型不计入成本估算。最近一次成功获取的官方价格目录会缓存至应用数据目录以供离线使用。

由于 Codex Session JSONL 文件目前未记录每个 Token 事件对应的用户/账号，因此账号配额卡片为按账号独立显示，而历史本地 Token 及成本汇总为本台机器上所有账号的合并数据。

## 运行开发

```bash
nvm use
npm install
npm run dev
```

## 打包构建

```bash
npm run build:mac:arm
npm run build:mac:x64
npm run build:win:x64
```

构建产物输出至 `src-tauri/target/<target>/release/bundle/`。

Mac arm64 和 x64 安装包建议在 macOS 环境下构建。Windows x64 安装包可在 Windows 本地或通过 GitHub Actions 进行构建。

## GitHub Actions

- 每次 Pull Request 都会自动运行 `npm ci` 和 `npm run lint`。
- 发布前先同步更新 `package.json`、`package-lock.json`、`src-tauri/Cargo.toml`、`src-tauri/Cargo.lock` 和 `src-tauri/tauri.conf.json` 中的版本号。推送代码至 `master` 或 `main` 分支（或手动触发 workflow）后，工作流会按项目版本创建对应的 Git Tag（例如 `1.1.0` 对应 `v1.1.0`），构建多平台安装包（macOS arm64、macOS x64、Windows x64），并将构建产物附加到 GitHub Releases。若对应 Tag 已存在，工作流会停止并要求先更新项目版本。

## 数据存储路径

默认情况下，账号数据存储在：

```text
data/
  accounts.json
  accounts/
    <account-id>/auth.json
```

如有需要，可以通过环境变量 `CODEX_SWITCH_DATA_DIR` 覆盖此存储路径。

## 平台说明

本项目以 macOS 优先并兼顾 Windows 支持。核心的文件复制与原子切换逻辑使用 Rust 后端 API 实现，保持平台无关性。

## 开源协议

本项目采用 [MIT License](LICENSE) 开源协议。
