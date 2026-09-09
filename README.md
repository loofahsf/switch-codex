# Switch Codex

Switch Codex 是一款基于 Wails v3 构建、以 macOS 优先的多 Codex `auth.json` 账号配置管理与快速切换桌面应用。

前端使用 TypeScript、React、Ant Design、AntV 和 Vite，核心账号、统计和调度逻辑由 Go 后端完成。Wails CLI 与运行时固定为 `v3.0.0-beta.18`。

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

用量配额查询使用与官方 Codex 客户端相同的只读 ChatGPT Codex 用量接口及账号 Header。配额和调度所用凭证仅在 Go 后端读取，不会返回给渲染进程或写入日志。Session 日志文件仅在本地解析，仅提取时间戳、模型名称和 Token 计数器。

由于 ChatGPT/Codex 付费计划属于订阅制产品，界面展示的美元金额为对比参考估算值，而非实际账单。未公布价格的模型不计入成本估算。最近一次成功获取的官方价格目录会缓存至应用数据目录以供离线使用。

由于 Codex Session JSONL 文件目前未记录每个 Token 事件对应的用户/账号，因此账号配额卡片为按账号独立显示，而历史本地 Token 及成本汇总为本台机器上所有账号的合并数据。

## 运行开发

```bash
nvm use
npm install
npm run desktop:setup
npm run dev
```

## 打包构建

```bash
npm run build:mac:arm
npm run build:mac:x64
npm run build:win:x64
```

安装包输出至 `release/`，中间产物位于 `bin/`。macOS 生成 DMG，Windows 生成 NSIS EXE，不再生成 MSI。

Mac arm64 和 x64 安装包建议在 macOS 环境下构建。Windows x64 安装包可在 Windows 本地或通过 GitHub Actions 进行构建。

## GitHub Actions

- Pull Request 和分支推送会运行前端检查与桥接测试、Go 测试、`go vet`、核心包竞态检测、网页构建及 Wails 绑定同步检查。
- `package.json` 是应用版本的唯一来源；修改版本后运行 `npm run version:sync` 生成 Go、macOS 和 Windows 元数据。
- 推送至 `master` 或 `main`（或手动触发 Release workflow）会先验证并构建 macOS arm64、macOS x64、Windows x64 三个安装包。三者全部成功后才创建对应 Tag 和正式 Release。直接构建已有 Tag 时会校验该 Tag、版本和提交完全一致。

## 数据存储路径

开发模式的数据存储在项目 `data/`。正式版继续沿用旧版目录：macOS 为 `~/Library/Application Support/com.switchcodex.app/data`，Windows 为 `%LOCALAPPDATA%/com.switchcodex.app/data`。

目录格式保持不变：

```text
data/
  accounts.json
  accounts/
    <account-id>/auth.json
```

环境变量 `CODEX_SWITCH_DATA_DIR` 始终优先。开发模式仅发现旧 `src-tauri/data` 时会复制并校验后使用 `data/`，旧目录保留；两处都有账号时会停止自动迁移并要求明确指定目录。

## 平台说明

本项目支持 macOS 12 及以上版本（arm64、x64）和 Windows 10 及以上版本（x64）。核心文件替换、进程清理和文件锁分别提供 Unix/Windows 实现。

## 开源协议

本项目采用 [MIT License](LICENSE) 开源协议。

## 设置与每日定时调用

在侧边栏 **设置** 页面开启“每日定时调用”，通过 24 小时制 TimePicker 选择电脑本地时间（`HH:mm:ss`）并保存，已有 `HH:mm` 设置会自动补为 `HH:mm:00`。默认关闭。应用会每天通过本机 Codex CLI，对已保存账号依次调用 `gpt-5.6-luna`，固定提示词为 `What model are you?`。

- 后台仍每分钟检查一次，在设定的时分秒之后首次轮询时启动，并非精确到秒触发。整批任务允许最多 5 分钟的启动延迟，超过则跳过当天。
- 开始时一次性获取账号及认证快照，按账号列表顺序串行执行，并发数始终为 1。五分钟限制只针对整批启动，后面的账号排队超过五分钟仍会执行。
- 每个账号调用最多等待 120 秒，失败或超时会继续下一个账号，不自动重试。页面显示最近一批账号的等待中、执行中、成功、失败或中断状态。点击账号整行（或键盘 Enter／空格）可打开详情弹窗，查看实际提示词、已收到的完整助手响应、起止时间和错误信息；弹窗随状态更新。
- 每个本地日期最多启动一批，修改时间不会重复运行当天已启动的任务。关闭功能或修改设置仅影响未来批次，正在执行的队列会继续。
- 应用退出、电脑休眠期间错过的任务不补跑。macOS 关闭窗口后应用仍留在菜单栏运行；从菜单退出应用或 Windows 关闭窗口会终止任务，重启不恢复未完成队列。请保持电脑清醒并联网。
- 执行期间新增账号留待下次；删除或更新账号不改变本批快照，但会阻止旧任务覆盖已更新的凭证。

任务使用独立临时 `CODEX_HOME`，不会切换当前账号或替换 `~/.codex/auth.json`。CLI 自动刷新的认证信息会在身份匹配且原凭证未被修改时原子回写至对应账号文件；临时目录完成后清理，异常退出残留会在下次启动清理。凭证不返回前端、不写日志。由于独立调用使用 `--ephemeral`，不会增加本地 Codex Session 历史；服务端仍正常计入订阅额度。

一次调用的成功仅代表 CLI 返回成功完成事件，不能保证重置已开启的五小时窗口。窗口起止时间由服务端决定，任务完成后会刷新额度数据。

需要安装支持 `codex exec`、`--ignore-user-config`、`--ephemeral`、`--json` 等参数的 Codex CLI。应用自动从 PATH 和常见安装目录查找，设置中未填写自定义路径时，输入框 placeholder 会显示自动找到的实际路径；找不到时给出提示。提示路径不保存为自定义值，清空自定义路径会重新查找。也可填写可执行文件绝对路径；Windows 指定 `codex.exe`，不使用 `.cmd` 脚本。启用时会检查 CLI 参数兼容性，不会自动安装或登录。

构建需要 Go 1.26、Node.js 22.12 以上版本和 npm；不再需要 Rust 工具链。同一账号数据目录仅允许一个应用实例使用，避免重复启动任务。

设置和最近一次执行结果存储在账号数据目录的 `settings.json`，临时认证目录为 `scheduled-runtime/`；均跟随 `CODEX_SWITCH_DATA_DIR`。开发模式忽略账号数据、价格缓存、临时凭证和构建产物，避免触发 Wails 重启。

最近一批账号记录会保存实际传给 CLI 的提示词和助手响应正文，以纯文本显示，不包含推理内容、工具事件或原始日志。失败或超时会保留已收到的完整助手消息，输出超过现有 1 MiB 上限时明确提示。旧版记录没有保存这些内容，会显示“该历史记录未保存此内容”，不会补造历史响应。
