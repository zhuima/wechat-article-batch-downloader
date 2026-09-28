<p align="center">
  <img src="verification/app-icon.png" width="112" alt="公众号文章下载器图标">
</p>

<h1 align="center">公众号文章下载器</h1>

<p align="center">在 Windows 和 Mac 上读取并批量归档有权访问的微信公众号历史文章，保存为 Markdown、HTML、纯文本和 JSONL 语料。</p>

<p align="center">
  <a href="#windows-1011"><strong>Windows 使用说明</strong></a>
  ·
  <a href="https://github.com/dingaiminGIT/wechat-article-batch-downloader/releases/latest/download/MPArticleDownloader-macOS-arm64.zip"><strong>下载 macOS 应用</strong></a>
  ·
  <a href="DESKTOP.md">macOS 安装说明</a>
  ·
  <a href="https://github.com/dingaiminGIT/wechat-article-batch-downloader/releases">全部版本</a>
</p>

> 支持 Windows 10/11 的 WinForms + WebView2 桌面客户端，以及 macOS 14 及以上、Apple Silicon 的原生应用。macOS 安装包尚未经过 Apple 公证，第一次打开时请按下文完成系统确认。

## 1.0.3 更新

- 修复部分公众号明明有多篇文章，历史接口却返回成功空页并触发 `unexpected end of JSON input` 的问题。
- 兼容微信当前的作者文章列表：自动识别 `author_id`，并使用 `from_article_id` 游标读取后续页面。
- 旧历史接口首屏为空时自动切换到作者列表，不再把“0 篇”误判为读取完成。
- 网页内备用面板同步使用新链路；读取中断时保留已发现的文章和进度，并显示面向用户的继续提示。
- 诊断日志不再输出文章列表会话 Cookie。

![公众号文章下载器主界面](verification/desktop-home.png)

## 它解决什么问题

微信公众号适合阅读，却不适合整理大量历史内容。公众号文章下载器把“逐篇打开、复制、保存图片、整理文件名”变成一次本地归档：在 Windows 上从电脑微信复制文章链接并导入，或在 macOS 应用中打开目标公众号文章，即可读取当前账号有权访问的历史列表，并把文章批量保存到本机。

它不要求你管理目标公众号，适合个人资料备份、写作研究、知识库整理和 AI 语料准备。

## 主要功能

- **读取全部历史**：支持最近文章、全部历史和日期范围，读取中可暂停并从断点继续。
- **四种本地输出**：每篇文章同时生成 Markdown、HTML 和 TXT，并维护一份去重的 `style_corpus.jsonl`。
- **正文与图片一起保存**：Markdown 引用本地图片，HTML 保留排版并可离线打开。
- **安全 / 快速模式**：安全模式适合大批量长期归档；快速模式遇到短暂访问验证会自动降速重试。
- **跳过已有文章**：使用稳定文章标识去重，重复下载时可跳过或覆盖。
- **清楚的下载中心**：按公众号显示 `已完成/总数`、失败数、开始与结束时间、总耗时和平均速度。
- **一个公众号一个目录**：目录直接使用公众号名称，可从界面一键打开。
- **后台静默运行（macOS）**：不再弹出终端窗口；关闭主窗口后仍可继续下载。

## Windows 10/11

下载 Windows 发布包并解压，双击其中的 **`MPArticleDownloader\windows\MPArticleDownloader.exe`**。它是 WinForms + WebView2 桌面客户端，启动时会让你选择“复制文章链接导入”或“连接微信并自动识别文章”，随后在自己的窗口中显示文章库。关闭窗口时会结束由客户端启动的本地服务。发布包已包含程序，无需安装 Go。

电脑需要 **.NET Framework 4.8** 和 **Microsoft Edge WebView2 Runtime**。Windows 11 通常已具备 WebView2；若启动提示缺少运行时，可从 [微软官方下载页](https://developer.microsoft.com/microsoft-edge/webview2/#download-section) 安装 Evergreen Runtime。发布包中的 `start.cmd` 是备用启动方式：它使用独立的 Edge 应用窗口，需要安装 Microsoft Edge，并且要保持命令窗口打开、按 **Ctrl+C** 退出。

从源码运行时，在仓库根目录打开 PowerShell 或命令提示符，执行：

```powershell
.\windows\start.cmd
```

备用启动器从源码首次运行时会自动编译程序，需要 **Go 1.22 或更新版本**，并能够下载 Go 依赖。桌面客户端和备用 Edge 窗口都只加载本机 `http://127.0.0.1:2132/desktop` 文章库。

如果要批量读取历史，在客户端起始页选择“连接微信并自动识别文章”。客户端显示文章库后，在电脑微信打开目标公众号文章，让客户端获取该次打开文章的会话。随后从“公众号列表”进入该公众号读取历史。历史能否取得，仍以微信实际返回的列表为准。

如果只需保存当前文章，可选择“复制文章链接导入”。这个模式**不启用 Windows 系统代理，也不需要安装证书**：

1. 在电脑微信中打开目标公众号任意一篇有权访问的文章，复制完整链接。
2. 在客户端窗口粘贴链接，点击“导入公众号”，然后从左侧选择该公众号。
3. 点击“下载这篇文章”保存刚导入的文章。如果链接含有可用的历史会话，客户端也会尝试读取列表；单有链接参数不代表历史已可读取。
4. 在“下载中心”查看进度。完成后，从公众号页面的“本地文档”或历史列表中点击已下载文章的标题，即可在客户端查看本地 Markdown 和图片；也可以打开输出目录查看原文件。即使微信没有返回历史列表，已下载的单篇文章仍会列在“本地文档”。

客户端启动时会检测正在监听的 Clash Verge HTTP/Mixed 端口，把它作为网络上游，并临时将 Windows 系统代理指向本机文章捕获服务。若 Clash 随后改动系统代理，自动识别可能失效；此时请关闭 Clash 的“系统代理”开关并重新启动客户端。Clash TUN 若接管电脑微信流量，实际捕获情况仍需以新打开文章能否出现在客户端为准。

有些文章没有可直接使用的历史入口。客户端会尝试微信提供的备用作者列表，并核对返回文章属于同一公众号；若无法核实，仍可下载当前文章。作者列表可能只覆盖该作者的文章，实际范围以微信返回的内容为准。

默认输出到 `%USERPROFILE%\Downloads\公众号文章归档\<公众号名称>\`。如需指定位置，可运行 `.\windows\start.cmd -DownloadDir "D:\公众号归档"`。配置、证书和诊断日志保存在 `%LOCALAPPDATA%\MPArticleDownloader\`。

需要自动识别时，在客户端起始页选择“连接微信并自动识别文章”。此模式会把本机生成的连接证书加入**当前用户**的受信任根证书列表，并临时修改当前用户的系统代理；关闭客户端时恢复原设置。首次出现证书确认提示时，请核对发行者 `MP Article Batch Downloader Local CA`。已有本地服务运行时不能切换连接方式；请先退出原服务，再重新打开客户端选择。备用命令行入口为 `.\windows\start.cmd -UseSystemProxy`。

应用未运行时，可执行 Windows 启动自检；它会启动本地服务、检查接口，然后退出：

```powershell
.\windows\start.cmd -SmokeTest
```

维护者可在装有 Go 1.22+、.NET Framework 4.8 编译器的 Windows 电脑上生成发布包。脚本会从微软 NuGet 下载并校验固定版本的 WebView2 SDK：

```powershell
.\windows\build-release.ps1
```

生成文件为 `dist\MPArticleDownloader-Windows-x64.zip`，内含 WinForms 客户端、WebView2 组件、后台程序、备用启动器、README 和许可证。

## macOS 下载与安装

### 1. 下载

[**下载公众号文章下载器 for macOS（Apple Silicon）**](https://github.com/dingaiminGIT/wechat-article-batch-downloader/releases/latest/download/MPArticleDownloader-macOS-arm64.zip)

下载后解压，把“公众号文章下载器”拖入“应用程序”目录。

### 2. 第一次打开

当前版本没有 Apple 公证。如果 macOS 阻止打开：

1. 在 Finder 中尝试打开一次应用。
2. 打开“系统设置 → 隐私与安全性”。
3. 在安全提示旁点击“仍要打开”。

应用第一次连接微信时，会将一张仅在本机生成的连接证书加入当前用户的信任列表，不需要管理员密码；证书保持不变时，后续连接不需要重复操作。

### 3. 开始归档

1. 在应用中点击“连接微信”。
2. 在电脑微信里打开目标公众号任意一篇文章。
3. 回到应用，从左侧选择识别到的公众号。
4. 选择“全部历史”或其他范围，点击“读取文章”。
5. 选择安全或快速模式，开始下载。
6. 在“下载中心”查看进度，完成后点击“打开目录”。

公众号历史接口使用微信文章页里的临时凭证。凭证失效时，重新在微信中打开该公众号的一篇文章即可更新连接。

## 实测速度

在同一台 Apple Silicon Mac 上完整归档“每天晒白牙”210篇文章：

| 模式 | 结果 | 总耗时 | 平均速度 |
|---|---:|---:|---:|
| 安全模式 | 210/210 | 3分35秒 | 约58.6篇/分钟 |
| 快速模式 | 210/210 | **1分53秒** | **110.7篇/分钟** |

实际速度受文章图片数量、网络状况和微信访问限制影响。快速模式出现短暂访问验证时会自动降速重试；持续受限时会暂停剩余任务，避免产生大量失败记录。

## 输出目录

默认保存到 Windows 的 `%USERPROFILE%\Downloads\公众号文章归档\`，或 macOS 的 `~/Downloads/公众号文章归档/`。每个公众号的目录结构为：

```text
<公众号名称>/
├── html/                 # 保留排版的离线 HTML
├── markdown/             # Markdown 正文
│   └── images/           # 本地图片
├── text/                 # 纯文本
└── style_corpus.jsonl    # 一行一篇，适合检索、分析和 AI 处理
```

## 隐私与安全

- 所有文章和图片保存在本机，应用没有账号系统，也不会上传你的文章库。
- 本地 API 只监听 `127.0.0.1`，并限制微信文章页和回环地址来源。
- 每台电脑会生成独立证书与私钥，私钥不随安装包或源码分发。
- Windows 默认不修改系统代理；开启 `-UseSystemProxy` 后，退出启动器会尝试恢复原代理配置。
- 工具不会绕过付费墙；未购买、已删除或被微信限制的内容无法保证导出。

请只归档你本人有权访问的内容，并遵守版权、平台规则和适用法律。

## macOS 从源码构建

需要 Xcode、Swift 6 和 Go 1.22 或更新版本：

```bash
git clone https://github.com/dingaiminGIT/wechat-article-batch-downloader.git
cd wechat-article-batch-downloader
./script/build_and_run.sh --install
```

构建与验证细节见 [macOS 应用说明](DESKTOP.md)、[实现说明](IMPLEMENTATION.md)和[实机验证记录](VERIFICATION.md)。原命令行入口仍保留给需要自行配置端口和运行方式的用户。

## 许可

本项目是在 `wx_channels_download` 基础上继续开发的衍生作品，沿用上游的 **Commons Clause + MIT** 条款。源码可以查看、修改和非商业使用，但禁止将软件或实质相似的服务用于销售。完整条款见 [LICENSE](LICENSE)。
