using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Drawing;
using System.Globalization;
using System.IO;
using System.Net;
using System.Security.Cryptography;
using System.Text;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;
using System.Web.Script.Serialization;
using System.Windows.Forms;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

// The WeRead session stays inside a separate InPrivate WebView2 profile. Only
// article metadata crosses into the local archive; authentication data never
// enters an application request, file, diagnostic message, or log.
internal sealed class WeReadWindow : Form
{
    private const string WeReadHome = "https://weread.qq.com/";
    private const string LocalService = "http://127.0.0.1:2132";
    private static readonly Regex BookIDPattern = new Regex(@"^MP_WXS_[0-9]{1,32}$", RegexOptions.CultureInvariant);
    private static readonly Regex BizPattern = new Regex(@"^M[A-Za-z0-9+/_=-]{5,255}$", RegexOptions.CultureInvariant);

    private readonly WebView2 reader;
    private readonly TextBox bizInput;
    private readonly TextBox bookInput;
    private readonly Button locateButton;
    private readonly Button scanButton;
    private readonly Button pauseButton;
    private readonly Label stateLabel;
    private readonly TextBox details;
    private readonly JavaScriptSerializer json = new JavaScriptSerializer { MaxJsonLength = 4 * 1024 * 1024 };
    private CancellationTokenSource delayCancellation;
    private bool busy;
    private bool pauseRequested;
    private bool ready;
    private readonly bool autoScanRequested;
    private bool autoScanStarted;

    private sealed class Page
    {
        internal int Reviews;
        internal int Skipped;
        internal List<Dictionary<string, object>> Articles;
        internal string Fingerprint;
    }

    internal WeReadWindow() : this("", "") { }

    internal WeReadWindow(string biz, string bookID)
    {
        Text = "微信读书文章列表（试验）";
        StartPosition = FormStartPosition.CenterParent;
        Size = new Size(1200, 900);
        MinimumSize = new Size(900, 630);

        var root = new TableLayoutPanel { Dock = DockStyle.Fill, ColumnCount = 1, RowCount = 3 };
        root.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 100));
        root.RowStyles.Add(new RowStyle(SizeType.Absolute, 102));
        root.RowStyles.Add(new RowStyle(SizeType.Percent, 74));
        root.RowStyles.Add(new RowStyle(SizeType.Percent, 26));

        var controls = new TableLayoutPanel { Dock = DockStyle.Fill, ColumnCount = 1, RowCount = 2, Padding = new Padding(9, 8, 9, 2) };
        controls.RowStyles.Add(new RowStyle(SizeType.Absolute, 42));
        controls.RowStyles.Add(new RowStyle(SizeType.Percent, 100));
        var actions = new FlowLayoutPanel { Dock = DockStyle.Fill, WrapContents = false, AutoScroll = true };
        actions.Controls.Add(NewLabel("公众号 biz"));
        bizInput = new TextBox { Width = 170, Margin = new Padding(3, 7, 13, 3) };
        actions.Controls.Add(bizInput);
        actions.Controls.Add(NewLabel("微信读书 bookId"));
        bookInput = new TextBox { Width = 215, Margin = new Padding(3, 7, 13, 3) };
        bizInput.Text = biz;
        bookInput.Text = bookID;
        autoScanRequested = biz.Length > 0 && bookID.Length > 0;
        actions.Controls.Add(bookInput);
        locateButton = new Button { Text = "定位阅读器", AutoSize = true, Enabled = false, Margin = new Padding(3, 3, 7, 3) };
        locateButton.Click += (sender, args) => LocateReader();
        actions.Controls.Add(locateButton);
        scanButton = new Button { Text = "读取并保存文章列表", AutoSize = true, Enabled = false, Margin = new Padding(3, 3, 7, 3) };
        scanButton.Click += async (sender, args) => await ScanAsync();
        actions.Controls.Add(scanButton);
        pauseButton = new Button { Text = "暂停", AutoSize = true, Enabled = false };
        pauseButton.Click += (sender, args) => RequestPause();
        actions.Controls.Add(pauseButton);
        controls.Controls.Add(actions, 0, 0);
        stateLabel = new Label
        {
            Dock = DockStyle.Fill,
            ForeColor = Color.FromArgb(62, 72, 66),
            Text = "正在打开微信读书。请在下方网页用手机扫码登录；登录后填入目标公众号的 biz 和 bookId。",
            TextAlign = ContentAlignment.MiddleLeft
        };
        controls.Controls.Add(stateLabel, 0, 1);

        reader = new WebView2 { Dock = DockStyle.Fill };
        details = new TextBox
        {
            Dock = DockStyle.Fill,
            Multiline = true,
            ReadOnly = true,
            ScrollBars = ScrollBars.Vertical,
            Font = new Font("Microsoft YaHei UI", 9),
            Text = "这里显示文章标题与分页进度，不显示登录凭证。\r\n"
        };
        root.Controls.Add(controls, 0, 0);
        root.Controls.Add(reader, 0, 1);
        root.Controls.Add(details, 0, 2);
        Controls.Add(root);
        Shown += async (sender, args) => await InitializeReaderAsync();
    }

    private static Label NewLabel(string value)
    {
        return new Label { Text = value, AutoSize = true, Margin = new Padding(3, 10, 3, 0) };
    }

    private async Task InitializeReaderAsync()
    {
        try
        {
            string profile = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
                "MPArticleDownloader", "weread-private-webview2");
            Directory.CreateDirectory(profile);
            // This environment does not traverse the app's local HTTPS capture
            // proxy when automatic WeChat detection is enabled.
            var environmentOptions = new CoreWebView2EnvironmentOptions("--no-proxy-server");
            var environment = await CoreWebView2Environment.CreateAsync(null, profile, environmentOptions);
            if (IsDisposed) return;
            var controllerOptions = environment.CreateCoreWebView2ControllerOptions();
            controllerOptions.ProfileName = "WereadInPrivate";
            controllerOptions.IsInPrivateModeEnabled = true;
            await reader.EnsureCoreWebView2Async(environment, controllerOptions);
            if (IsDisposed) return;
            reader.CoreWebView2.Settings.IsWebMessageEnabled = false;
            reader.CoreWebView2.Settings.AreHostObjectsAllowed = false;
            reader.CoreWebView2.NavigationStarting += GuardNavigation;
            reader.CoreWebView2.NewWindowRequested += GuardNewWindow;
            reader.CoreWebView2.DocumentTitleChanged += (sender, args) => TryStartAutoScan();
            reader.CoreWebView2.NavigationCompleted += (sender, args) => TryStartAutoScan();
            ready = true;
            string bookID = bookInput.Text.Trim();
            reader.CoreWebView2.Navigate(BookIDPattern.IsMatch(bookID) ? ReaderURL(bookID) : WeReadHome);
            SetBusy(false);
        }
        catch (Exception error)
        {
            ShowState("微信读书窗口启动失败：" + error.Message);
        }
    }

    private static bool IsWeReadPage(string raw)
    {
        Uri uri;
        return Uri.TryCreate(raw, UriKind.Absolute, out uri) && uri.Scheme == Uri.UriSchemeHttps &&
            string.Equals(uri.Host, "weread.qq.com", StringComparison.OrdinalIgnoreCase);
    }

    private static string Md5Hex(string value)
    {
        using (var md5 = MD5.Create())
        {
            byte[] digest = md5.ComputeHash(Encoding.UTF8.GetBytes(value));
            var hex = new StringBuilder(digest.Length * 2);
            foreach (byte item in digest) hex.Append(item.ToString("x2", CultureInfo.InvariantCulture));
            return hex.ToString();
        }
    }

    // Mirrors WeRead's current generateMpReaderUrl({bookId}) for MP_WXS_*.
    // Keep the two check vectors in validation when the upstream site changes.
    private static string ReaderURL(string bookID)
    {
        string md5 = Md5Hex(bookID);
        var body = new StringBuilder();
        foreach (char item in bookID) body.Append(((int)item).ToString("x", CultureInfo.InvariantCulture));
        string bodyHex = body.ToString();
        string hash = md5.Substring(0, 3) + "42" + md5.Substring(md5.Length - 2) +
            bodyHex.Length.ToString("x2", CultureInfo.InvariantCulture) + bodyHex;
        if (hash.Length < 20) hash += md5.Substring(0, 20 - hash.Length);
        hash += Md5Hex(hash).Substring(0, 3);
        return "https://weread.qq.com/web/mp/reader/" + hash;
    }

    private void TryStartAutoScan()
    {
        if (!autoScanRequested || autoScanStarted || busy || !ready || IsDisposed || reader.CoreWebView2 == null) return;
        Uri uri, expected;
        if (!Uri.TryCreate(reader.CoreWebView2.Source, UriKind.Absolute, out uri) ||
            !Uri.TryCreate(ReaderURL(bookInput.Text.Trim()), UriKind.Absolute, out expected) ||
            uri.AbsolutePath != expected.AbsolutePath ||
            reader.CoreWebView2.DocumentTitle.IndexOf("公众号", StringComparison.Ordinal) < 0) return;
        autoScanStarted = true;
        BeginInvoke(new Action(async () => await ScanAsync()));
    }

    private static bool IsAllowedNavigation(string raw)
    {
        Uri uri;
        if (!Uri.TryCreate(raw, UriKind.Absolute, out uri) || uri.Scheme != Uri.UriSchemeHttps) return false;
        string host = uri.Host.ToLowerInvariant();
        return host == "weread.qq.com" || host == "open.weixin.qq.com" ||
            host.EndsWith(".weixin.qq.com", StringComparison.Ordinal) || host == "ptlogin2.qq.com";
    }

    private void GuardNavigation(object sender, CoreWebView2NavigationStartingEventArgs args)
    {
        if (!IsAllowedNavigation(args.Uri)) args.Cancel = true;
    }

    private void GuardNewWindow(object sender, CoreWebView2NewWindowRequestedEventArgs args)
    {
        args.Handled = true;
        if (IsAllowedNavigation(args.Uri)) reader.CoreWebView2.Navigate(args.Uri);
    }

    private void ShowState(string value)
    {
        if (!IsDisposed) stateLabel.Text = value;
    }

    private void WriteDetail(string value)
    {
        if (IsDisposed) return;
        if (details.TextLength > 32000) details.Text = details.Text.Substring(details.TextLength - 16000);
        details.AppendText(value + "\r\n");
    }

    private string ValidBookID()
    {
        string value = bookInput.Text.Trim();
        if (!BookIDPattern.IsMatch(value)) throw new InvalidOperationException("bookId 格式应为 MP_WXS_ 后接数字。");
        return value;
    }

    private string ValidBiz()
    {
        string value = bizInput.Text.Trim();
        if (!BizPattern.IsMatch(value)) throw new InvalidOperationException("公众号 biz 格式无效。请填写文章链接中的 __biz 值。");
        return value;
    }

    private static void RequireMatchingAccount(string biz, string bookID)
    {
        try
        {
            string base64 = biz.Replace('-', '+').Replace('_', '/');
            string numeric = Encoding.ASCII.GetString(Convert.FromBase64String(base64.PadRight((base64.Length + 3) / 4 * 4, '=')));
            if (Regex.IsMatch(numeric, @"^[0-9]+$") && bookID == "MP_WXS_" + numeric) return;
        }
        catch (FormatException) { }
        throw new InvalidOperationException("biz 与 bookId 不属于同一个公众号，请核对后重试。");
    }

    private async Task<Dictionary<string, object>> EvaluateAsync(string expression)
    {
        if (!ready || reader.CoreWebView2 == null || !IsWeReadPage(reader.CoreWebView2.Source))
            throw new InvalidOperationException("请先在微信读书网页完成登录。");
        string parameters = json.Serialize(new Dictionary<string, object>
        {
            { "expression", expression }, { "awaitPromise", true }, { "returnByValue", true }
        });
        string response = await reader.CoreWebView2.CallDevToolsProtocolMethodAsync("Runtime.evaluate", parameters);
        var envelope = AsObject(json.DeserializeObject(response));
        if (envelope.ContainsKey("exceptionDetails")) throw new InvalidOperationException("微信读书页面执行失败，请刷新阅读器后重试。");
        var result = AsObject(Get(envelope, "result"));
        var value = AsObject(Get(result, "value"));
        return value;
    }

    private static Dictionary<string, object> AsObject(object value)
    {
        var result = value as Dictionary<string, object>;
        if (result == null) throw new InvalidOperationException("微信读书返回的数据格式不符合预期。");
        return result;
    }

    private static object Get(Dictionary<string, object> data, string key)
    {
        object value;
        return data.TryGetValue(key, out value) ? value : null;
    }

    private static string StringValue(object value)
    {
        return value == null ? "" : Convert.ToString(value, CultureInfo.InvariantCulture);
    }

    private static int IntValue(object value)
    {
        if (value == null) return 0;
        int number;
        return int.TryParse(StringValue(value), NumberStyles.Integer, CultureInfo.InvariantCulture, out number) ? number : 0;
    }

    private static bool BoolValue(object value)
    {
        return value is bool && (bool)value;
    }

    private static void CheckPageResult(Dictionary<string, object> result)
    {
        if (BoolValue(Get(result, "ok"))) return;
        int errorCode = IntValue(Get(result, "errCode"));
        if (errorCode == -2010) throw new InvalidOperationException("微信读书尚未登录或登录已过期，请在窗口内扫码登录。");
        if (errorCode == -2041) throw new InvalidOperationException("微信读书拒绝了当前页面请求，请先打开该号的阅读器并等待页面加载完成。");
        if (errorCode != 0) throw new InvalidOperationException("微信读书返回错误码 " + errorCode + "；已保存的分页不会丢失。");
        string error = StringValue(Get(result, "error"));
        throw new InvalidOperationException(error.Length > 0 ? error : "微信读书请求失败；已保存的分页不会丢失。");
    }

    private void SetBusy(bool value)
    {
        busy = value;
        locateButton.Enabled = !value && ready;
        scanButton.Enabled = !value && ready;
        pauseButton.Enabled = value;
        bizInput.Enabled = !value;
        bookInput.Enabled = !value;
    }

    private void LocateReader()
    {
        if (busy) return;
        SetBusy(true);
        try
        {
            string bookID = ValidBookID();
            reader.CoreWebView2.Navigate(ReaderURL(bookID));
            ShowState("已打开目标公众号阅读器。等待标题显示“公众号”后点击“读取并保存文章列表”。");
        }
        catch (Exception error)
        {
            ShowState(error.Message);
            WriteDetail(error.Message);
        }
        finally { SetBusy(false); }
    }

    private async Task IsReaderReadyAsync(string bookID)
    {
        Uri current, expected;
        if (!Uri.TryCreate(reader.CoreWebView2.Source, UriKind.Absolute, out current) ||
            !Uri.TryCreate(ReaderURL(bookID), UriKind.Absolute, out expected) ||
            current.AbsolutePath != expected.AbsolutePath)
            throw new InvalidOperationException("当前阅读器与输入的 bookId 不一致，请点击“定位阅读器”。");
        string expression = "(function(){var reader=location.pathname.indexOf('/web/mp/reader/')===0;" +
            "var nodes=document.querySelectorAll('[id*=captcha],[class*=tcaptcha],iframe[src*=captcha]');var captcha=false;" +
            "for(var i=0;i<nodes.length;i++){var visible=true;for(var n=nodes[i];n;n=n.parentElement){" +
            "var s=getComputedStyle(n);if(s.display==='none'||s.visibility==='hidden'||Number(s.opacity)===0){visible=false;break}}" +
            "var box=nodes[i].getBoundingClientRect();if(visible&&box.width>0&&box.height>0){captcha=true;break}}" +
            "return {ok:true,reader:reader,title:document.title,captcha:captcha}})()";
        Dictionary<string, object> result = await EvaluateAsync(expression);
        if (BoolValue(Get(result, "captcha"))) throw new InvalidOperationException("微信读书需要访问验证。请在窗口内完成验证后继续读取。");
        if (!BoolValue(Get(result, "reader"))) throw new InvalidOperationException("请先打开目标公众号的微信读书阅读器。");
        string title = StringValue(Get(result, "title"));
        if (title.IndexOf("公众号", StringComparison.Ordinal) < 0)
            throw new InvalidOperationException("阅读器还没有加载完成，请稍候再点击读取。");
    }

    // One call performs exactly one same-origin request. It does not expose
    // cookies or arbitrary response headers to the native or local backend.
    private async Task<Page> GetPageAsync(string bookID, int offset)
    {
        string expression = "(function(){var url='/web/mp/articles?bookId='+encodeURIComponent(" + json.Serialize(bookID) + ")+'&offset=" + offset.ToString(CultureInfo.InvariantCulture) + "';" +
            "return fetch(url,{credentials:'include'}).then(function(r){if(!r.ok)return {ok:false,error:'文章接口 HTTP '+r.status};return r.json()})" +
            ".then(function(o){if(!o||o.ok===false)return o;if(o.errCode)return {ok:false,errCode:o.errCode};" +
            "if(!Array.isArray(o.reviews))return {ok:false,error:'文章列表格式异常'};var articles=[],skipped=0;" +
            "o.reviews.forEach(function(group){(group.subReviews||[]).forEach(function(sub){var rev=sub.review||{},info=rev.mpInfo||{};" +
            "var original=String(info.originalId||'').replace(/~/g,'_');if(!info.title||!/^[A-Za-z0-9_-]+$/.test(original)){skipped++;return;}" +
            "var stamp=Number(rev.createTime||group.createTime||0);if(stamp>1000000000000)stamp=Math.floor(stamp/1000);" +
            "articles.push({title:String(info.title),url:'https://mp.weixin.qq.com/s/'+original,published:Math.max(0,Math.floor(stamp))})})});" +
            "var first=articles[0]||{},last=articles[articles.length-1]||{};return {ok:true,reviews:o.reviews.length,articles:articles,skipped:skipped," +
            "fingerprint:String(first.url||'')+'|'+String(last.url||'')+'|'+o.reviews.length}})" +
            ".catch(function(){return {ok:false,error:'文章请求失败，请检查网络或访问验证'}})})()";
        Dictionary<string, object> result = await EvaluateAsync(expression);
        CheckPageResult(result);
        int reviews = IntValue(Get(result, "reviews"));
        int skipped = IntValue(Get(result, "skipped"));
        if (reviews < 0 || reviews > 100) throw new InvalidOperationException("单页群发数量异常，已停止读取。");
        var items = Get(result, "articles") as object[];
        if (items == null) throw new InvalidOperationException("文章列表缺少文章数组，已停止读取。");
        var articles = new List<Dictionary<string, object>>();
        foreach (object item in items)
        {
            var article = AsObject(item);
            string url = StringValue(Get(article, "url"));
            Uri uri;
            if (!Uri.TryCreate(url, UriKind.Absolute, out uri) || uri.Scheme != Uri.UriSchemeHttps ||
                uri.Host != "mp.weixin.qq.com" || !uri.AbsolutePath.StartsWith("/s/", StringComparison.Ordinal)) continue;
            articles.Add(article);
        }
        if (reviews > 0 && articles.Count == 0)
            throw new InvalidOperationException("该页有群发记录但未得到有效文章链接，已停止，避免误报读取完成。");
        return new Page { Reviews = reviews, Skipped = skipped, Articles = articles, Fingerprint = StringValue(Get(result, "fingerprint")) };
    }

    private async Task ScanAsync()
    {
        if (busy) return;
        SetBusy(true);
        pauseRequested = false;
        delayCancellation = new CancellationTokenSource();
        try
        {
            string biz = ValidBiz();
            string bookID = ValidBookID();
            RequireMatchingAccount(biz, bookID);
            await IsReaderReadyAsync(bookID);
            int offset = await SavedOffsetAsync(biz, bookID);
            if (offset < 0) return;
            var fingerprints = new HashSet<string>(StringComparer.Ordinal);
            int pages = 0;
            while (!pauseRequested)
            {
                if (IsDisposed) return;
                await IsReaderReadyAsync(bookID);
                Page page = await GetPageAsync(bookID, offset);
                if (page.Reviews > 0 && !fingerprints.Add(page.Fingerprint))
                    throw new InvalidOperationException("微信读书重复返回同一页，已停止以避免循环。已保存的分页仍在本地。");
                await SavePageAsync(biz, bookID, offset, page);
                pages++;
                offset += page.Reviews;
                ShowState("已保存 " + pages + " 页；本页 " + page.Reviews + " 组、" + page.Articles.Count + " 篇；下次从 offset=" + offset + " 继续。" + (page.Skipped > 0 ? " 另有 " + page.Skipped + " 条缺少可用直链。" : ""));
                WriteDetail("第 " + pages + " 页 · offset=" + (offset - page.Reviews) + " · " + page.Reviews + " 组 · " + page.Articles.Count + " 篇" + (page.Skipped > 0 ? " · " + page.Skipped + " 条缺链接" : ""));
                foreach (var article in page.Articles) WriteDetail("  " + StringValue(Get(article, "title")));
                if (page.Reviews == 0) { ShowState("微信读书收录列表已读到末页。结果可能不是公众号完整历史。"); break; }
                if (pages >= 100) throw new InvalidOperationException("已达到本次 100 页上限，请关闭窗口后从保存的 offset 继续。");
                try { await Task.Delay(3000, delayCancellation.Token); }
                catch (TaskCanceledException) { break; }
            }
            if (pauseRequested) ShowState("已暂停。已经导入的文章与 offset 已保存；下次点击读取可继续。");
        }
        catch (Exception error)
        {
            ShowState("读取已停止：" + error.Message);
            WriteDetail("读取已停止：" + error.Message);
        }
        finally
        {
            delayCancellation.Dispose();
            delayCancellation = null;
            SetBusy(false);
        }
    }

    private void RequestPause()
    {
        if (!busy) return;
        pauseRequested = true;
        if (delayCancellation != null) delayCancellation.Cancel();
        ShowState("正在暂停；当前页若已收到，会先保存到本地。");
    }

    private async Task<int> SavedOffsetAsync(string biz, string bookID)
    {
        string body = await Task.Run(() => LocalRequest("GET", "/api/desktop/weread/scan?biz=" + Uri.EscapeDataString(biz), null));
        var envelope = AsObject(json.DeserializeObject(body));
        if (IntValue(Get(envelope, "code")) != 0) throw new InvalidOperationException("读取本地断点失败。");
        object rawData = Get(envelope, "data");
        if (rawData == null) return 0;
        var data = AsObject(rawData);
        string savedBookID = StringValue(Get(data, "book_id"));
        if (savedBookID.Length > 0 && savedBookID != bookID)
            throw new InvalidOperationException("现有断点属于另一个 bookId，请核对目标公众号。");
        string status = StringValue(Get(data, "status"));
        if (status == "complete" || status == "partial")
        {
            ShowState("该公众号的微信读书列表状态为 " + status + "，已有结果。此次未重复读取。");
            return -1;
        }
        if (status.Length > 0 && status != "idle" && status != "paused")
            throw new InvalidOperationException("本地读取状态为 " + status + "，请先处理该状态后再继续。");
        int offset = IntValue(Get(data, "offset"));
        if (offset < 0) throw new InvalidOperationException("本地分页断点无效。");
        ShowState(offset > 0 ? "从已保存的 offset=" + offset + " 继续读取。" : "从第一页开始读取。");
        return offset;
    }

    private async Task SavePageAsync(string biz, string bookID, int offset, Page page)
    {
        var payload = new Dictionary<string, object>
        {
            { "biz", biz }, { "book_id", bookID }, { "offset", offset },
            { "reviews", page.Reviews }, { "articles", page.Articles }
        };
        string body = json.Serialize(payload);
        string response = await Task.Run(() => LocalRequest("POST", "/api/desktop/weread/page", body));
        var envelope = AsObject(json.DeserializeObject(response));
        if (IntValue(Get(envelope, "code")) != 0)
            throw new InvalidOperationException("本页无法保存到本地服务；已经保存的前几页仍保留。");
    }

    private static string LocalRequest(string method, string path, string body)
    {
        var request = (HttpWebRequest)WebRequest.Create(LocalService + path);
        request.Proxy = null;
        request.Method = method;
        request.Timeout = 15000;
        if (body != null)
        {
            byte[] bytes = Encoding.UTF8.GetBytes(body);
            request.ContentType = "application/json; charset=utf-8";
            request.ContentLength = bytes.Length;
            using (Stream stream = request.GetRequestStream()) stream.Write(bytes, 0, bytes.Length);
        }
        using (var response = (HttpWebResponse)request.GetResponse())
        using (var reader = new StreamReader(response.GetResponseStream(), Encoding.UTF8))
        {
            if (response.StatusCode != HttpStatusCode.OK) throw new InvalidOperationException("本地服务返回 HTTP " + (int)response.StatusCode);
            return reader.ReadToEnd();
        }
    }

    protected override void OnFormClosing(FormClosingEventArgs args)
    {
        if (busy)
        {
            RequestPause();
            args.Cancel = true;
            return;
        }
        base.OnFormClosing(args);
    }
}
