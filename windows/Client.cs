using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Drawing;
using System.IO;
using System.Net;
using System.Text;
using System.Text.RegularExpressions;
using System.Threading.Tasks;
using System.Web.Script.Serialization;
using System.Windows.Forms;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

internal static class Program
{
    [STAThread]
    private static void Main()
    {
        Application.EnableVisualStyles();
        Application.SetCompatibleTextRenderingDefault(false);
        Application.Run(new DesktopWindow());
    }
}

internal sealed class DesktopWindow : Form
{
    private const string BaseUrl = "http://127.0.0.1:2132";
    private const int DesktopProtocolVersion = 7;
    private const string OldServiceMessage = "检测到旧版后台服务。请先退出旧客户端或启动窗口，再启动新版客户端。";
    private readonly WebView2 browser;
    private readonly Label status;
    private readonly TableLayoutPanel connectionChoice;
    private readonly MenuStrip menu;
    private readonly StringBuilder launcherOutput = new StringBuilder();
    private readonly object outputLock = new object();
    private Process launcher;
    private WeReadWindow wereadWindow;
    private bool choiceInProgress;

    private sealed class ServiceInfo
    {
        internal bool Ready;
        internal bool? ProxyCaptureActive;
    }

    internal DesktopWindow()
    {
        Text = "公众号文章归档";
        StartPosition = FormStartPosition.CenterScreen;
        // Keep the restored window as wide and tall as the reference layout.
        // On smaller displays, use the available work area so the title bar
        // and controls remain reachable; maximizing is intentionally unrestricted.
        Rectangle workArea = Screen.PrimaryScreen.WorkingArea;
        Size restoredSize = new Size(Math.Min(1387, workArea.Width), Math.Min(1103, workArea.Height));
        MinimumSize = restoredSize;
        Size = restoredSize;
        WindowState = FormWindowState.Maximized;
        BackColor = Color.White;

        browser = new WebView2 { Dock = DockStyle.Fill, Visible = false };
        status = new Label
        {
            Dock = DockStyle.Fill,
            Visible = false,
            BackColor = Color.White,
            ForeColor = Color.FromArgb(42, 54, 66),
            Font = new Font("Microsoft YaHei UI", 11),
            TextAlign = ContentAlignment.MiddleCenter,
            Text = "正在启动公众号文章归档…"
        };
        connectionChoice = CreateConnectionChoice();
        menu = new MenuStrip { Dock = DockStyle.Top };
        var wereadItem = new ToolStripMenuItem("微信读书文章列表（试验）");
        wereadItem.Click += (sender, args) => OpenWeReadWindow("", "");
        menu.Items.Add(wereadItem);
        Controls.Add(browser);
        Controls.Add(status);
        Controls.Add(connectionChoice);
        Controls.Add(menu);
        MainMenuStrip = menu;
        Shown += async (sender, args) =>
        {
            try
            {
                ServiceInfo existing = await Task.Run(() => ReadServiceInfo());
                if (!IsDisposed && existing.Ready)
                {
                    connectionChoice.Visible = false;
                    status.Visible = true;
                    await InitializeAsync(false, false);
                }
            }
            catch (InvalidOperationException error)
            {
                if (!IsDisposed) connectionChoice.GetControlFromPosition(0, 4).Text = error.Message;
            }
        };
    }

    private TableLayoutPanel CreateConnectionChoice()
    {
        var panel = new TableLayoutPanel
        {
            Dock = DockStyle.Fill,
            BackColor = Color.FromArgb(247, 250, 249),
            Padding = new Padding(42, 48, 42, 34),
            ColumnCount = 1,
            RowCount = 5
        };
        panel.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 100));
        panel.RowStyles.Add(new RowStyle(SizeType.Absolute, 100));
        panel.RowStyles.Add(new RowStyle(SizeType.Absolute, 96));
        panel.RowStyles.Add(new RowStyle(SizeType.Absolute, 78));
        panel.RowStyles.Add(new RowStyle(SizeType.Absolute, 78));
        panel.RowStyles.Add(new RowStyle(SizeType.Percent, 100));

        panel.Controls.Add(new Label
        {
            Dock = DockStyle.Fill,
            Text = "连接微信公众号",
            Font = new Font("Microsoft YaHei UI", 21, FontStyle.Bold),
            ForeColor = Color.FromArgb(30, 57, 45),
            TextAlign = ContentAlignment.MiddleCenter
        }, 0, 0);
        panel.Controls.Add(new Label
        {
            Dock = DockStyle.Fill,
            Text = "选择一种连接方式。链接导入更稳定；自动识别需要临时设置当前用户的系统代理。",
            Font = new Font("Microsoft YaHei UI", 10),
            ForeColor = Color.FromArgb(76, 94, 84),
            TextAlign = ContentAlignment.MiddleCenter
        }, 0, 1);

        var linkButton = new Button
        {
            Size = new Size(480, 54),
            Anchor = AnchorStyles.None,
            Text = "复制文章链接导入（推荐）",
            BackColor = Color.FromArgb(39, 111, 76),
            ForeColor = Color.White,
            FlatStyle = FlatStyle.Flat,
            Font = new Font("Microsoft YaHei UI", 11, FontStyle.Bold)
        };
        linkButton.FlatAppearance.BorderSize = 0;
        linkButton.Click += async (sender, args) => await BeginConnectionAsync(false);
        panel.Controls.Add(linkButton, 0, 2);

        var proxyButton = new Button
        {
            Size = new Size(480, 54),
            Anchor = AnchorStyles.None,
            Text = "连接微信并自动识别文章",
            BackColor = Color.FromArgb(224, 239, 229),
            ForeColor = Color.FromArgb(30, 78, 52),
            FlatStyle = FlatStyle.Flat,
            Font = new Font("Microsoft YaHei UI", 11, FontStyle.Bold)
        };
        proxyButton.FlatAppearance.BorderColor = Color.FromArgb(116, 159, 129);
        proxyButton.Click += async (sender, args) => await BeginConnectionAsync(true);
        panel.Controls.Add(proxyButton, 0, 3);

        panel.Controls.Add(new Label
        {
            Dock = DockStyle.Fill,
            Text = "链接导入：在电脑微信打开一篇公众号文章，复制完整链接，粘贴到下一页。",
            Font = new Font("Microsoft YaHei UI", 9),
            ForeColor = Color.FromArgb(104, 117, 109),
            TextAlign = ContentAlignment.TopCenter,
            Padding = new Padding(0, 24, 0, 0)
        }, 0, 4);
        return panel;
    }

    private async Task BeginConnectionAsync(bool useSystemProxy)
    {
        if (choiceInProgress) return;
        choiceInProgress = true;
        ServiceInfo existing;
        try { existing = await Task.Run(() => ReadServiceInfo()); }
        catch (InvalidOperationException error)
        {
            if (!IsDisposed) MessageBox.Show(this, error.Message, "请先退出旧版服务", MessageBoxButtons.OK, MessageBoxIcon.Information);
            choiceInProgress = false;
            return;
        }
        if (useSystemProxy && existing.Ready && existing.ProxyCaptureActive != true)
        {
            if (!IsDisposed) MessageBox.Show(this,
                "已有本地服务正在运行，无法在运行中切换系统代理。请先退出原来的启动窗口或客户端，再重新打开并选择自动识别。",
                "请先退出已有服务", MessageBoxButtons.OK, MessageBoxIcon.Information);
            choiceInProgress = false;
            return;
        }
        if (IsDisposed) return;
        if (useSystemProxy)
        {
            string explanation = existing.Ready
                ? "检测到现有服务已启用自动识别。此窗口会直接连接它，不会重复安装证书或设置代理。关闭此窗口不会关闭现有服务或恢复代理；请从原来的启动窗口或客户端退出服务。\r\n\r\n若微信流量被 Clash TUN 等接管，仍可能无法自动识别；此时可在页面中粘贴文章链接。"
                : "自动识别会安装一张仅用于本机连接的当前用户证书，并在运行期间把当前用户的系统代理指向本程序。关闭客户端后会恢复原代理设置。\r\n\r\n若微信流量被 Clash TUN 等接管，仍可能无法自动识别；此时可在页面中粘贴文章链接。";
            DialogResult confirmed = MessageBox.Show(this, explanation,
                "启用自动识别", MessageBoxButtons.OKCancel, MessageBoxIcon.Information);
            if (confirmed != DialogResult.OK) { choiceInProgress = false; return; }
        }
        connectionChoice.Visible = false;
        status.Visible = true;
        await InitializeAsync(useSystemProxy, useSystemProxy && existing.Ready);
    }

    private async Task InitializeAsync(bool useSystemProxy, bool attachExistingCapture)
    {
        try
        {
            try { CoreWebView2Environment.GetAvailableBrowserVersionString(null); }
            catch (Exception error)
            {
                throw new InvalidOperationException(
                    "没有找到 Microsoft Edge WebView2 Runtime。请从微软官网下载并安装，或使用同目录的 start.cmd 备用启动器。",
                    error);
            }
            ServiceInfo serviceInfo = await Task.Run(() => ReadServiceInfo());
            bool ready = serviceInfo.Ready;
            if (attachExistingCapture && !ready)
                throw new InvalidOperationException("现有自动识别服务已退出。请重新打开客户端并选择自动识别。");
            if (useSystemProxy && ready && serviceInfo.ProxyCaptureActive != true)
                throw new InvalidOperationException("已有本地服务启动，请退出该服务后重新选择自动识别。 ");
            if (!ready)
            {
                if (IsDisposed) return;
                StartService(useSystemProxy);
                for (int attempt = 0; attempt < 120 && !ready; attempt++)
                {
                    if (IsDisposed) return;
                    await Task.Delay(250);
                    if (IsDisposed) return;
                    ready = await Task.Run(() => IsServiceReady());
                    if (!ready && launcher.HasExited)
                        throw new InvalidOperationException("本地服务启动失败。" + GetLauncherOutput());
                }
                if (ready && useSystemProxy)
                {
                    // If another launcher won the port race, only attach when
                    // that service has already enabled proxy capture.
                    await Task.Delay(500);
                    if (IsDisposed) return;
                    if (launcher.HasExited)
                    {
                        if (await Task.Run(() => ReadProxyCaptureActive() != true))
                            throw new InvalidOperationException("已有本地服务先于自动识别模式启动。请退出该服务后重试。");
                    }
                    bool proxyActive = false;
                    for (int attempt = 0; attempt < 40 && !proxyActive; attempt++)
                    {
                        if (IsDisposed) return;
                        proxyActive = await Task.Run(() => ReadProxyCaptureActive() == true);
                        if (!proxyActive && launcher.HasExited)
                            throw new InvalidOperationException("自动识别启动失败。" + GetLauncherOutput());
                        if (!proxyActive) await Task.Delay(250);
                    }
                    if (!proxyActive)
                        throw new TimeoutException("系统代理没有切换到本机连接服务，无法确认自动识别已启用。" + GetLauncherOutput());
                }
            }
            if (!ready) throw new TimeoutException("等待本地服务启动超时。" + GetLauncherOutput());
            if (IsDisposed) return;

            string profile = Path.Combine(
                Environment.GetFolderPath(Environment.SpecialFolder.MyDocuments),
                "MPArticleDownloaderData", "webview2-profile");
            Directory.CreateDirectory(profile);
            CoreWebView2Environment environment = await CoreWebView2Environment.CreateAsync(null, profile);
            if (IsDisposed) return;
            await browser.EnsureCoreWebView2Async(environment);
            if (IsDisposed) return;
            browser.CoreWebView2.NewWindowRequested += OpenExternalWindow;
            browser.CoreWebView2.NavigationStarting += OpenExternalNavigation;
            browser.CoreWebView2.WebMessageReceived += HandleDesktopMessage;
            browser.CoreWebView2.Navigate(BaseUrl + "/desktop");
            browser.Visible = true;
            status.Visible = false;
        }
        catch (Exception error)
        {
            if (IsDisposed) return;
            status.Text = "启动失败\r\n\r\n" + error.Message +
                          "\r\n\r\n可尝试双击同目录的 start.cmd 查看详细错误。";
        }
    }

    private void StartService(bool useSystemProxy)
    {
        string script = Path.Combine(AppDomain.CurrentDomain.BaseDirectory, "start.ps1");
        if (!File.Exists(script)) throw new FileNotFoundException("找不到 Windows 启动脚本。", script);
        string powershell = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.System),
                                         "WindowsPowerShell", "v1.0", "powershell.exe");
        if (!File.Exists(powershell)) throw new FileNotFoundException("找不到 Windows PowerShell。", powershell);

        var start = new ProcessStartInfo
        {
            FileName = powershell,
            Arguments = "-NoProfile -ExecutionPolicy Bypass -File \"" + script +
                        "\" -NoBrowser -ClientPid " + Process.GetCurrentProcess().Id +
                        (useSystemProxy ? " -UseSystemProxy" : ""),
            WorkingDirectory = AppDomain.CurrentDomain.BaseDirectory,
            UseShellExecute = false,
            CreateNoWindow = true,
            WindowStyle = ProcessWindowStyle.Hidden,
            RedirectStandardOutput = true,
            RedirectStandardError = true
        };
        launcher = new Process { StartInfo = start };
        launcher.OutputDataReceived += CaptureOutput;
        launcher.ErrorDataReceived += CaptureOutput;
        if (!launcher.Start()) throw new InvalidOperationException("无法启动本地服务。 ");
        launcher.BeginOutputReadLine();
        launcher.BeginErrorReadLine();
    }

    private void CaptureOutput(object sender, DataReceivedEventArgs args)
    {
        if (string.IsNullOrEmpty(args.Data)) return;
        lock (outputLock)
        {
            if (launcherOutput.Length < 8000) launcherOutput.AppendLine(args.Data);
        }
    }

    private string GetLauncherOutput()
    {
        lock (outputLock) return "\r\n" + launcherOutput.ToString().Trim();
    }

    private static bool IsServiceReady()
    {
        return ReadServiceInfo().Ready;
    }

    private static ServiceInfo ReadServiceInfo()
    {
        try
        {
            var request = (HttpWebRequest)WebRequest.Create(BaseUrl + "/api/desktop/info");
            request.Proxy = null;
            request.Timeout = 800;
            using (var response = (HttpWebResponse)request.GetResponse())
            using (var reader = new StreamReader(response.GetResponseStream()))
            {
                if (response.StatusCode != HttpStatusCode.OK) return new ServiceInfo();
                string body = reader.ReadToEnd();
                if (!Regex.IsMatch(body, "\"app\"\\s*:\\s*\"mp-archive-desktop\"")) return new ServiceInfo();
                if (!Regex.IsMatch(body, "\"version\"\\s*:\\s*" + DesktopProtocolVersion + "(?=\\s*[,}])"))
                    throw new InvalidOperationException(OldServiceMessage);
                Match match = Regex.Match(body, "\"proxy_capture_active\"\\s*:\\s*(true|false)", RegexOptions.IgnoreCase);
                return new ServiceInfo
                {
                    Ready = true,
                    ProxyCaptureActive = match.Success
                        ? (bool?)string.Equals(match.Groups[1].Value, "true", StringComparison.OrdinalIgnoreCase)
                        : null
                };
            }
        }
        catch (WebException) { return new ServiceInfo(); }
    }

    private static bool? ReadProxyCaptureActive()
    {
        try
        {
            ServiceInfo info = ReadServiceInfo();
            return info.Ready ? info.ProxyCaptureActive : null;
        }
        catch { return null; }
    }

    private static void OpenExternalWindow(object sender, CoreWebView2NewWindowRequestedEventArgs args)
    {
        args.Handled = true;
        OpenExternalUrl(args.Uri);
    }

    private void OpenWeReadWindow(string biz, string bookID)
    {
        if (wereadWindow != null && !wereadWindow.IsDisposed)
        {
            wereadWindow.Activate();
            return;
        }
        wereadWindow = new WeReadWindow(biz, bookID);
        wereadWindow.FormClosed += (sender, args) => wereadWindow = null;
        wereadWindow.Show(this);
    }

    private void HandleDesktopMessage(object sender, CoreWebView2WebMessageReceivedEventArgs args)
    {
        Uri source;
        if (!Uri.TryCreate(args.Source, UriKind.Absolute, out source) ||
            source.Scheme != Uri.UriSchemeHttp || source.Host != "127.0.0.1" ||
            source.Port != 2132 || source.AbsolutePath != "/desktop") return;
        try
        {
            var data = new JavaScriptSerializer().DeserializeObject(args.WebMessageAsJson) as Dictionary<string, object>;
            if (data == null) return;
            object rawType, rawBiz, rawBookID;
            if (!data.TryGetValue("type", out rawType) || !data.TryGetValue("biz", out rawBiz) ||
                !data.TryGetValue("bookId", out rawBookID)) return;
            string type = rawType as string, biz = rawBiz as string, bookID = rawBookID as string;
            if (type != "weread-start" || biz == null || bookID == null ||
                !Regex.IsMatch(biz, @"^M[A-Za-z0-9+/_=-]{5,255}$") ||
                !Regex.IsMatch(bookID, @"^MP_WXS_[0-9]{1,32}$")) return;
            string base64 = biz.Replace('-', '+').Replace('_', '/');
            string decoded = Encoding.ASCII.GetString(Convert.FromBase64String(base64.PadRight((base64.Length + 3) / 4 * 4, '=')));
            if (!Regex.IsMatch(decoded, @"^[0-9]+$") || bookID != "MP_WXS_" + decoded) return;
            OpenWeReadWindow(biz, bookID);
        }
        catch (ArgumentException) { /* Ignore malformed page messages. */ }
        catch (FormatException) { /* Ignore malformed page messages. */ }
        catch (InvalidOperationException) { /* Ignore malformed page messages. */ }
    }

    private static void OpenExternalNavigation(object sender, CoreWebView2NavigationStartingEventArgs args)
    {
        if (args.Uri.StartsWith(BaseUrl + "/", StringComparison.OrdinalIgnoreCase)) return;
        args.Cancel = true;
        OpenExternalUrl(args.Uri);
    }

    private static void OpenExternalUrl(string url)
    {
        Uri parsed;
        if (!Uri.TryCreate(url, UriKind.Absolute, out parsed) ||
            (parsed.Scheme != Uri.UriSchemeHttp && parsed.Scheme != Uri.UriSchemeHttps)) return;
        try { Process.Start(new ProcessStartInfo(parsed.AbsoluteUri) { UseShellExecute = true }); }
        catch { /* An unavailable default browser must not close the client. */ }
    }

    protected override void OnFormClosing(FormClosingEventArgs args)
    {
        if (launcher != null)
        {
            try
            {
                if (!launcher.HasExited)
                {
                    if (ShutdownService())
                    {
                        if (!launcher.WaitForExit(12000)) launcher.Kill();
                    }
                    else launcher.Kill();
                    launcher.WaitForExit(2000);
                }
            }
            catch { /* The backend also watches this process and restores proxy settings. */ }
            launcher.Dispose();
            launcher = null;
        }
        base.OnFormClosing(args);
    }

    private static bool ShutdownService()
    {
        try
        {
            var request = (HttpWebRequest)WebRequest.Create(BaseUrl + "/api/desktop/shutdown");
            request.Proxy = null;
            request.Method = "POST";
            request.ContentLength = 0;
            request.Timeout = 2000;
            using (var response = (HttpWebResponse)request.GetResponse())
                return response.StatusCode == HttpStatusCode.OK;
        }
        catch (WebException) { return false; }
    }
}
