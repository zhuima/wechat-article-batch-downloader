param(
    [Parameter(Mandatory = $true)] [string] $OutputDir,
    [string] $PackagePath
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$version = '1.0.4191.47'
$packageSha256 = 'F492BBF547D0DA329553B6727435B677579B1E9F91CC9E4A1AD029366D5F23D0'
$csc = Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'
if (-not (Test-Path -LiteralPath $csc -PathType Leaf)) {
    throw '需要 Windows .NET Framework 4.8 的 C# 编译器（Framework64\v4.0.30319\csc.exe）。'
}

if (-not $PackagePath) {
    $PackagePath = Join-Path ([System.IO.Path]::GetTempPath()) ('microsoft.web.webview2.' + $version + '.nupkg')
    if (-not (Test-Path -LiteralPath $PackagePath)) {
        $url = 'https://api.nuget.org/v3-flatcontainer/microsoft.web.webview2/' + $version +
               '/microsoft.web.webview2.' + $version + '.nupkg'
        [System.Net.ServicePointManager]::SecurityProtocol = [System.Net.SecurityProtocolType]::Tls12
        Write-Host ('正在下载 Microsoft WebView2 SDK ' + $version + '...')
        Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $PackagePath
    }
}
$PackagePath = [System.IO.Path]::GetFullPath($PackagePath)
if (-not (Test-Path -LiteralPath $PackagePath -PathType Leaf)) { throw '找不到 WebView2 SDK NuGet 包。' }
$actualSha256 = (Get-FileHash -LiteralPath $PackagePath -Algorithm SHA256).Hash
if ($actualSha256 -ne $packageSha256) {
    throw ('WebView2 SDK 校验失败。预期 SHA-256：' + $packageSha256 + '，实际：' + $actualSha256)
}

$OutputDir = [System.IO.Path]::GetFullPath($OutputDir)
New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
$stage = Join-Path ([System.IO.Path]::GetTempPath()) ('MPArticleDownloader-WebView2-' + [guid]::NewGuid().ToString('N'))
$tempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\') + '\'
$resolvedStage = [System.IO.Path]::GetFullPath($stage)
if (-not $resolvedStage.StartsWith($tempRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw 'WebView2 SDK 临时目录不在系统临时目录下。'
}

try {
    New-Item -ItemType Directory -Path $stage -Force | Out-Null
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    [System.IO.Compression.ZipFile]::ExtractToDirectory($PackagePath, $stage)
    $core = Join-Path $stage 'lib\net462\Microsoft.Web.WebView2.Core.dll'
    $winforms = Join-Path $stage 'lib\net462\Microsoft.Web.WebView2.WinForms.dll'
    $loader = Join-Path $stage 'runtimes\win-x64\native\WebView2Loader.dll'
    foreach ($file in @($core, $winforms, $loader)) {
        if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw ('WebView2 SDK 缺少文件：' + $file) }
    }

    $binary = Join-Path $OutputDir 'MPArticleDownloader.exe'
    Write-Host '正在编译 WebView2 Windows 客户端...'
    & $csc /nologo /target:winexe /platform:x64 /optimize+ "/out:$binary" `
        "/win32manifest:$(Join-Path $PSScriptRoot 'app.manifest')" `
        /reference:System.Windows.Forms.dll /reference:System.Drawing.dll /reference:System.Web.Extensions.dll `
        "/reference:$core" "/reference:$winforms" `
        (Join-Path $PSScriptRoot 'Client.cs') (Join-Path $PSScriptRoot 'WeReadWindow.cs')
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $binary)) {
        throw 'WinForms WebView2 客户端编译失败。'
    }

    Copy-Item -LiteralPath $core, $winforms -Destination $OutputDir
    $loaderDir = Join-Path $OutputDir 'runtimes\win-x64\native'
    New-Item -ItemType Directory -Path $loaderDir -Force | Out-Null
    Copy-Item -LiteralPath $loader -Destination $loaderDir
    Copy-Item -LiteralPath (Join-Path $stage 'LICENSE.txt') -Destination (Join-Path $OutputDir 'WebView2-LICENSE.txt')
    Copy-Item -LiteralPath (Join-Path $stage 'NOTICE.txt') -Destination (Join-Path $OutputDir 'WebView2-NOTICE.txt')
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'start.ps1') -Destination $OutputDir
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'storage.ps1') -Destination $OutputDir
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'start.cmd') -Destination $OutputDir
    Write-Host ('WebView2 客户端：' + $binary)
} finally {
    if (Test-Path -LiteralPath $resolvedStage) { Remove-Item -LiteralPath $resolvedStage -Recurse -Force }
}
