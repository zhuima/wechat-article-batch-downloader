param(
    [string] $OutputPath,
    [string] $WebView2PackagePath
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repo = Split-Path -Parent $PSScriptRoot
$module = Join-Path $repo 'mp_article_downloader_src'
$go = Get-Command go.exe -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty Source
if (-not $go -and (Test-Path -LiteralPath 'C:\Program Files\Go\bin\go.exe')) {
    $go = 'C:\Program Files\Go\bin\go.exe'
}
if (-not $go) { throw '构建 Windows 发布包需要 Go 1.22 或更新版本。' }

if (-not $OutputPath) {
    $OutputPath = Join-Path (Join-Path $repo 'dist') 'MPArticleDownloader-Windows-x64.zip'
}
$OutputPath = [System.IO.Path]::GetFullPath($OutputPath)
$stage = Join-Path ([System.IO.Path]::GetTempPath()) ('MPArticleDownloader-' + [guid]::NewGuid().ToString('N'))
$tempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\') + '\'
$resolvedStage = [System.IO.Path]::GetFullPath($stage)
if (-not $resolvedStage.StartsWith($tempRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw '发布包临时目录不在系统临时目录下。'
}
$app = Join-Path $stage 'MPArticleDownloader'
$build = Join-Path $app 'mp_article_downloader_src\build'
$winres = Join-Path $build 'winres'
$windows = Join-Path $app 'windows'
$verification = Join-Path $app 'verification'
$oldCgo = $env:CGO_ENABLED
$oldGoos = $env:GOOS
$oldGoarch = $env:GOARCH

try {
    New-Item -ItemType Directory -Path $build, $winres, $windows, $verification -Force | Out-Null
    & (Join-Path $PSScriptRoot 'build-client.ps1') -OutputDir $windows -PackagePath $WebView2PackagePath
    Copy-Item -LiteralPath (Join-Path $repo 'README.md') -Destination $app
    Copy-Item -LiteralPath (Join-Path $repo 'LICENSE') -Destination $app
    foreach ($file in @('DESKTOP.md', 'IMPLEMENTATION.md', 'VERIFICATION.md')) {
        Copy-Item -LiteralPath (Join-Path $repo $file) -Destination $app
    }
    foreach ($file in @('app-icon.png', 'desktop-home.png')) {
        Copy-Item -LiteralPath (Join-Path (Join-Path $repo 'verification') $file) -Destination $verification
    }
    # The favicon handler resolves this path relative to the service working directory.
    Copy-Item -LiteralPath (Join-Path $module 'build\icon.png') -Destination (Join-Path $winres 'icon.png')

    $env:CGO_ENABLED = '0'
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $binary = Join-Path $build 'mp_article_batch_downloader.exe'
    Write-Host '正在编译 Windows x64 程序...'
    & $go -C $module build -trimpath -o $binary .
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $binary)) {
        throw 'Go 构建失败，请查看上方错误。'
    }

    New-Item -ItemType Directory -Path (Split-Path -Parent $OutputPath) -Force | Out-Null
    Compress-Archive -LiteralPath $app -DestinationPath $OutputPath -Force
    Write-Host ('Windows 发布包：' + $OutputPath)
} finally {
    $env:CGO_ENABLED = $oldCgo
    $env:GOOS = $oldGoos
    $env:GOARCH = $oldGoarch
    if (Test-Path -LiteralPath $resolvedStage) { Remove-Item -LiteralPath $resolvedStage -Recurse -Force }
}
