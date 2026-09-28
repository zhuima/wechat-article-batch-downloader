param(
    [switch] $NoBrowser,
    [switch] $SmokeTest,
    [switch] $UseSystemProxy,
    [int] $ClientPid,
    [string] $DownloadDir
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repo = Split-Path -Parent $PSScriptRoot
$module = Join-Path $repo 'mp_article_downloader_src'
$binary = Join-Path $module 'build\mp_article_batch_downloader.exe'
. (Join-Path $PSScriptRoot 'storage.ps1')
$data = Get-MPDataRoot
$certDir = Join-Path $data 'certs'
$config = Join-Path $data 'config.yaml'
$url = 'http://127.0.0.1:2132'
$desktopProtocolVersion = '7'
$service = $null
$ownsService = $false

function Get-LocalJson([string] $path) {
    try {
        $request = [System.Net.HttpWebRequest]::Create($url + $path)
        $request.Proxy = $null
        $request.Timeout = 1000
        $response = $request.GetResponse()
        try {
            $reader = New-Object System.IO.StreamReader($response.GetResponseStream())
            try { return ($reader.ReadToEnd() | ConvertFrom-Json) }
            finally { $reader.Dispose() }
        } finally { $response.Dispose() }
    } catch { return $null }
}

function Send-Stop {
    try {
        $request = [System.Net.HttpWebRequest]::Create($url + '/api/desktop/shutdown')
        $request.Proxy = $null
        $request.Method = 'POST'
        $request.ContentLength = 0
        $request.Timeout = 2000
        $response = $request.GetResponse()
        $response.Dispose()
    } catch { }
}

function Test-LocalPort([int] $port) {
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $pending = $client.BeginConnect('127.0.0.1', $port, $null, $null)
        return $pending.AsyncWaitHandle.WaitOne(250) -and $client.Connected
    } catch { return $false }
    finally { $client.Dispose() }
}

function Get-ClashUpstreamProxy {
    $settingsPath = Join-Path $env:APPDATA 'io.github.clash-verge-rev.clash-verge-rev\clash-verge.yaml'
    if (-not (Test-Path -LiteralPath $settingsPath -PathType Leaf)) { return '' }

    # Read only top-level listener settings. Subscription entries can also
    # contain "port", so indented matches would select unrelated servers.
    $ports = @{}
    foreach ($line in [System.IO.File]::ReadLines($settingsPath)) {
        if ($line -match '^(mixed-port|port):\s*([0-9]{1,5})\s*(?:#.*)?$') {
            $ports[$matches[1]] = [int] $matches[2]
        }
    }
    foreach ($kind in @('mixed-port', 'port')) {
        if (-not $ports.ContainsKey($kind)) { continue }
        $candidatePort = $ports[$kind]
        if ($candidatePort -lt 1 -or $candidatePort -gt 65535 -or $candidatePort -in @(2132, 2133)) { continue }
        $listeners = @(Get-NetTCPConnection -LocalPort $candidatePort -State Listen -ErrorAction SilentlyContinue)
        foreach ($listener in $listeners) {
            if ($listener.LocalAddress -notin @('127.0.0.1', '::1')) { continue }
            $owner = Get-Process -Id $listener.OwningProcess -ErrorAction SilentlyContinue
            if ($owner -and $owner.ProcessName -eq 'verge-mihomo') {
                return ('http://127.0.0.1:' + $candidatePort)
            }
        }
    }
    return ''
}

function Get-EdgePath {
    foreach ($folder in @(${env:ProgramFiles(x86)}, $env:ProgramFiles, $env:LOCALAPPDATA)) {
        if (-not $folder) { continue }
        $candidate = Join-Path $folder 'Microsoft\Edge\Application\msedge.exe'
        if (Test-Path -LiteralPath $candidate -PathType Leaf) { return $candidate }
    }
    $command = Get-Command msedge.exe -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($command) { return $command.Source }
    throw '没有找到 Microsoft Edge。请先安装 Edge，再启动 Windows 客户端。'
}

function Open-DesktopWindow {
    $edge = Get-EdgePath
    $profileDir = Join-Path $data 'edge-profile'
    New-Item -ItemType Directory -Path $profileDir -Force | Out-Null
    # Start-Process joins ArgumentList into one command line. Keep the quotes so
    # --user-data-dir survives spaces and non-ASCII characters in the user path.
    $arguments = @(
        ('--app=' + $url + '/desktop')
        ('--user-data-dir="' + $profileDir + '"')
        '--no-first-run'
        '--no-default-browser-check'
    )
    Start-Process -FilePath $edge -ArgumentList $arguments | Out-Null
}

try {
    $existing = Get-LocalJson '/api/desktop/info'
    if ($existing -and $existing.data.app -eq 'mp-archive-desktop') {
        $versionProperty = $existing.data.PSObject.Properties['version']
        if (-not $versionProperty -or [string]$versionProperty.Value -ne $desktopProtocolVersion) {
            throw '检测到旧版后台服务。请先退出旧客户端或启动窗口，再启动新版客户端。'
        }
        if ($UseSystemProxy) { throw '下载器已经运行。请先退出当前启动窗口，再用 -UseSystemProxy 重新启动。' }
        if ($DownloadDir) { Write-Warning '下载器已经运行，-DownloadDir 需要退出后重新启动才会生效。' }
        if (-not $NoBrowser -and -not $SmokeTest) { Open-DesktopWindow }
        Write-Host '公众号文章下载器已经运行。'
        return
    }
    if ((Test-LocalPort 2132) -or (Test-LocalPort 2133)) {
        throw '端口 2132 或 2133 已被其他程序占用。请关闭占用程序后重试。'
    }

    $needsBuild = -not (Test-Path -LiteralPath $binary)
    if (-not $needsBuild -and (Test-Path -LiteralPath (Join-Path $module 'main.go'))) {
        $builtAt = (Get-Item -LiteralPath $binary).LastWriteTimeUtc
        $newerSource = Get-ChildItem -LiteralPath $module -Recurse -File |
            Where-Object { ($_.Extension -eq '.go' -or $_.Extension -eq '.html') -and $_.LastWriteTimeUtc -gt $builtAt } |
            Select-Object -First 1
        $needsBuild = $null -ne $newerSource
    }
    if ($needsBuild) {
        $go = (Get-Command go.exe -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty Source)
        if (-not $go -and (Test-Path -LiteralPath 'C:\Program Files\Go\bin\go.exe')) {
            $go = 'C:\Program Files\Go\bin\go.exe'
        }
        if (-not $go) {
            throw '需要构建 Windows 程序，但没有找到 Go。请安装 Go 1.22 或更新版本，或使用已编译的 Windows 发布包。'
        }
        Write-Host '正在构建 Windows 程序...'
        New-Item -ItemType Directory -Path (Split-Path -Parent $binary) -Force | Out-Null
        $env:CGO_ENABLED = '0'
        & $go -C $module build -trimpath -o $binary .
        if ($LASTEXITCODE -ne 0) { throw 'Go 构建失败，请查看上方错误。' }
    }

    # Wait until the old service has exited before reading its mutable files.
    # Both AppData views stay intact; the new root lives outside MSIX AppData
    # virtualization so Explorer and packaged hosts see the same archive.
    if ((Test-LocalPort 2132) -or (Test-LocalPort 2133)) {
        throw '端口 2132 或 2133 已被其他程序占用。请关闭占用程序后重试。'
    }
    Merge-MPStorage $data @(Get-MPLegacyDataRoots)
    New-Item -ItemType Directory -Path $data, $certDir -Force | Out-Null
    if (-not $DownloadDir) { $DownloadDir = Join-Path (Join-Path $env:USERPROFILE 'Downloads') '公众号文章归档' }
    $DownloadDir = [System.IO.Path]::GetFullPath($DownloadDir)
    New-Item -ItemType Directory -Path $DownloadDir -Force | Out-Null

    $clashUpstream = if ($UseSystemProxy) { Get-ClashUpstreamProxy } else { '' }
    $refreshBytes = New-Object byte[] 32
    $random = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $random.GetBytes($refreshBytes) }
    finally { $random.Dispose() }
    $refreshToken = [Convert]::ToBase64String($refreshBytes)
    # JSON is valid YAML; it keeps Windows paths and Chinese names unambiguous.
    $settings = @{
        api = @{ hostname = '127.0.0.1'; port = 2132; protocol = 'http' }
        proxy = @{ system = [bool] $UseSystemProxy; hostname = '127.0.0.1'; port = 2133; skipInstallRootCert = $true; upstreamProxy = $clashUpstream }
        download = @{ dir = $DownloadDir; playDoneAudio = $false }
        mp = @{ disabled = $false; refreshToken = $refreshToken }
        cert = @{
            name = 'MP Article Batch Downloader Local CA'
            file = (Join-Path $certDir 'root-ca.pem')
            key = (Join-Path $certDir 'root-ca-key.pem')
        }
    }
    $json = $settings | ConvertTo-Json -Depth 6
    [System.IO.File]::WriteAllText($config, $json, (New-Object System.Text.UTF8Encoding($false)))
    $env:MP_ARCHIVE_DATA = $data

    & $binary desktop-recover --config $config
    if ($LASTEXITCODE -ne 0) { throw '上次的系统代理配置恢复失败，请查看诊断日志。' }
    if ($UseSystemProxy) {
        Write-Host '正在检查本机连接证书...'
        & $binary desktop-prepare --config $config
        if ($LASTEXITCODE -ne 0) { throw '本机连接证书未能加入当前用户的信任列表。' }
    }

    if ($ClientPid -gt 1) { $env:MP_ARCHIVE_PARENT = [string] $ClientPid }
    else { $env:MP_ARCHIVE_PARENT = [string] $PID }
    $service = Start-Process -FilePath $binary -ArgumentList ('--config "' + $config + '"') -WorkingDirectory $module -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $data 'service.stdout.log') `
        -RedirectStandardError (Join-Path $data 'service.stderr.log')
    $ownsService = $true
    $ready = $false
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        Start-Sleep -Milliseconds 250
        $service.Refresh()
        if ($service.HasExited) { break }
        $info = Get-LocalJson '/api/desktop/info'
        if ($info -and $info.code -eq 0 -and $info.data.app -eq 'mp-archive-desktop') {
            $versionProperty = $info.data.PSObject.Properties['version']
            if (-not $versionProperty -or [string]$versionProperty.Value -ne $desktopProtocolVersion) {
                throw '检测到旧版后台服务占用端口。请先退出旧客户端或启动窗口，再启动新版客户端。'
            }
            $ready = $true
            break
        }
    }
    if (-not $ready) {
        throw ('服务启动失败。诊断日志：' + (Join-Path $data 'service.stderr.log'))
    }

    if ($UseSystemProxy) {
        # Clash Verge can write its own saved system-proxy state immediately
        # after our interceptor starts. Let that startup write settle, then
        # restore this session's proxy without replacing its original snapshot.
        Start-Sleep -Seconds 2
        & $binary desktop-ensure-proxy --config $config
        if ($LASTEXITCODE -ne 0) { throw '本机文章捕获代理未能重新接入系统代理。' }
        $captureInfo = Get-LocalJson '/api/desktop/info'
        if (-not $captureInfo -or $captureInfo.data.proxy_capture_active -ne $true) {
            throw '系统代理未指向本机文章捕获服务。请关闭 Clash 的系统代理开关后重新启动客户端。'
        }
    }

    if ($SmokeTest) {
        $status = Get-LocalJson '/api/status'
        if (-not $status -or $status.code -ne 0) { throw 'Windows 启动自检失败。' }
        if (-not (Test-LocalPort 2133)) { throw '微信连接代理没有启动。' }
        $request = [System.Net.HttpWebRequest]::Create($url + '/desktop')
        $request.Proxy = $null
        $request.Timeout = 2000
        $response = $request.GetResponse()
        try {
            if ([int] $response.StatusCode -ne 200 -or $response.ContentType -notlike 'text/html*') {
                throw '文章归档界面没有正常打开。'
            }
        } finally { $response.Dispose() }
        Write-Host 'Windows 启动自检通过。'
        return
    }
    if (-not $NoBrowser) { Open-DesktopWindow }
    Write-Host '公众号文章下载器已启动。请从电脑微信复制文章链接，在客户端窗口中导入公众号。'
    Write-Host ('文章库：' + $DownloadDir)
    if ($UseSystemProxy) {
        Write-Host '已确认系统代理接入本机文章捕获服务。按 Ctrl+C 退出时将恢复启动前的代理设置。'
        if ($clashUpstream) { Write-Host 'Clash 已作为本机代理的上游。' }
    }
    else { Write-Host '默认不修改系统代理。按 Ctrl+C 退出。' }
    while ($true) {
        Start-Sleep -Milliseconds 500
        $service.Refresh()
        if ($service.HasExited) { break }
    }
} catch {
    Write-Error $_
    exit 1
} finally {
    if ($service) {
        $service.Refresh()
        if (-not $service.HasExited) {
            Send-Stop
            if (-not $service.WaitForExit(5000)) { Stop-Process -Id $service.Id -Force -ErrorAction SilentlyContinue }
        }
    }
    if ($ownsService -and (Test-Path -LiteralPath $config) -and (Test-Path -LiteralPath $binary)) {
        & $binary desktop-recover --config $config | Out-Null
    }
}
