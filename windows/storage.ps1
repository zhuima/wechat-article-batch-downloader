# Shared by the WinForms launcher and the command-line launcher. Packaged
# processes can transparently redirect even an absolute AppData\Local path.
# Documents is outside that redirected tree and resolves to the same folder
# when this launcher is started from Explorer or from a packaged host.
# Windows PowerShell uses the .NET Framework serializer. System.Web.Extensions
# cannot deserialize even simple JSON under PowerShell 7 / modern .NET.
if ($PSVersionTable.PSVersion.Major -lt 6) {
    Add-Type -AssemblyName System.Web.Extensions
    $script:MPJsonSerializer = New-Object System.Web.Script.Serialization.JavaScriptSerializer
    $script:MPJsonSerializer.MaxJsonLength = [int]::MaxValue
}

function Get-MPDataRoot {
    $documents = [Environment]::GetFolderPath([Environment+SpecialFolder]::MyDocuments)
    if ([string]::IsNullOrWhiteSpace($documents)) {
        throw '无法找到当前用户的“文档”目录。'
    }
    return (Join-Path $documents 'MPArticleDownloaderData')
}

function Get-MPLegacyDataRoots {
    $profile = [Environment]::GetFolderPath([Environment+SpecialFolder]::UserProfile)
    if ([string]::IsNullOrWhiteSpace($profile)) { throw '无法找到当前用户目录。' }
    $local = Join-Path $profile 'AppData\Local'
    $candidates = New-Object 'System.Collections.Generic.List[string]'
    $candidates.Add((Join-Path $local 'MPArticleDownloader'))

    # The environment and shell API may point at a package-specific view.
    foreach ($base in @($env:LOCALAPPDATA, [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData))) {
        if (-not [string]::IsNullOrWhiteSpace($base)) {
            $candidates.Add((Join-Path $base 'MPArticleDownloader'))
        }
    }

    # A later launch from Explorer must still find data previously written by
    # the Codex packaged process. Only this host's known package family is read.
    $packages = Join-Path $local 'Packages'
    if (Test-Path -LiteralPath $packages -PathType Container) {
        foreach ($package in (Get-ChildItem -LiteralPath $packages -Directory -ErrorAction Stop | Where-Object { $_.Name -like 'OpenAI.Codex*' })) {
            $candidates.Add((Join-Path $package.FullName 'LocalCache\Local\MPArticleDownloader'))
        }
    }

    $seen = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    foreach ($candidate in $candidates) {
        $full = [IO.Path]::GetFullPath($candidate).TrimEnd('\')
        if ($seen.Add($full) -and (Test-Path -LiteralPath $full -PathType Container)) { $full }
    }
}

function Read-MPAccounts([string] $path) {
    try {
        $json = [IO.File]::ReadAllText($path, [Text.Encoding]::UTF8)
        if ($PSVersionTable.PSVersion.Major -ge 6) {
            # -AsHashtable preserves account IDs that differ only by case.
            $parsed = ConvertFrom-Json -InputObject $json -AsHashtable -ErrorAction Stop
        } else {
            $parsed = $script:MPJsonSerializer.DeserializeObject($json)
        }
    } catch {
        throw ('账户文件无法解析，迁移已停止：' + $path)
    }
    if ($null -eq $parsed -or $parsed -isnot [Collections.IDictionary]) {
        throw ('账户文件不是 JSON 对象，迁移已停止：' + $path)
    }
    return ,$parsed
}

function Get-MPAccountUpdateTime($account) {
    if ($null -eq $account -or $account -isnot [Collections.IDictionary] -or -not (@($account.Keys) -ccontains 'update_time')) { return 0L }
    $value = 0L
    if ([long]::TryParse([string]$account['update_time'], [ref]$value)) { return $value }
    return 0L
}

function Copy-MPMissingTree([string] $source, [string] $destination) {
    if (-not (Test-Path -LiteralPath $source -PathType Container)) { return }
    if (((Get-Item -LiteralPath $source).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { return }
    if (Test-Path -LiteralPath $destination) {
        if (-not (Test-Path -LiteralPath $destination -PathType Container)) {
            throw ('目标数据路径不是目录：' + $destination)
        }
        if (((Get-Item -LiteralPath $destination).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw ('目标数据目录是链接，迁移已停止：' + $destination)
        }
    } else {
        New-Item -ItemType Directory -Path $destination -Force | Out-Null
    }
    foreach ($item in (Get-ChildItem -LiteralPath $source -Force -ErrorAction Stop)) {
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { continue }
        $target = Join-Path $destination $item.Name
        if ($item.PSIsContainer) {
            Copy-MPMissingTree $item.FullName $target
        } elseif (-not (Test-Path -LiteralPath $target)) {
            [IO.File]::Copy($item.FullName, $target, $false)
        }
    }
}

function Test-MPMissingTree([string] $source, [string] $destination) {
    if (-not (Test-Path -LiteralPath $source -PathType Container)) { return $false }
    if (((Get-Item -LiteralPath $source).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { return $false }
    if (-not (Test-Path -LiteralPath $destination)) { return $true }
    if (-not (Test-Path -LiteralPath $destination -PathType Container)) {
        throw ('目标数据路径不是目录：' + $destination)
    }
    if (((Get-Item -LiteralPath $destination).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw ('目标数据目录是链接，迁移已停止：' + $destination)
    }
    foreach ($item in (Get-ChildItem -LiteralPath $source -Force -ErrorAction Stop)) {
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { continue }
        $target = Join-Path $destination $item.Name
        if ($item.PSIsContainer) {
            if (Test-MPMissingTree $item.FullName $target) { return $true }
        } elseif (-not (Test-Path -LiteralPath $target)) {
            return $true
        }
    }
    return $false
}

function Merge-MPStorage([string] $dataRoot, [string[]] $legacyRoots) {
    $dataRoot = [IO.Path]::GetFullPath($dataRoot).TrimEnd('\')
    $seen = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    $sources = New-Object 'System.Collections.Generic.List[object]'
    foreach ($legacyRoot in $legacyRoots) {
        if ([string]::IsNullOrWhiteSpace($legacyRoot)) { continue }
        $full = [IO.Path]::GetFullPath($legacyRoot).TrimEnd('\')
        if ($full -eq $dataRoot -or -not $seen.Add($full) -or -not (Test-Path -LiteralPath $full -PathType Container)) { continue }
        $accountPath = Join-Path $full 'mp.json'
        $accounts = $null
        $modified = [DateTime]::MinValue
        if (Test-Path -LiteralPath $accountPath -PathType Leaf) {
            $accounts = Read-MPAccounts $accountPath
            $modified = (Get-Item -LiteralPath $accountPath).LastWriteTimeUtc
        }
        $sources.Add([pscustomobject]@{ Root = $full; AccountPath = $accountPath; Accounts = $accounts; Modified = $modified })
    }
    if ($sources.Count -eq 0) { return }

    $accountTarget = Join-Path $dataRoot 'mp.json'
    $hasTarget = Test-Path -LiteralPath $accountTarget -PathType Leaf
    $merged = New-Object 'System.Collections.Generic.Dictionary[string,object]' ([StringComparer]::Ordinal)
    $scores = New-Object 'System.Collections.Generic.Dictionary[string,object]' ([StringComparer]::Ordinal)
    $canonicalKeys = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::Ordinal)
    if ($hasTarget) {
        $existing = Read-MPAccounts $accountTarget
        foreach ($key in $existing.Keys) {
            $merged.Add([string]$key, $existing[$key])
            [void]$canonicalKeys.Add([string]$key)
        }
    }
    foreach ($source in $sources) {
        if ($null -eq $source.Accounts) { continue }
        foreach ($key in $source.Accounts.Keys) {
            if ($canonicalKeys.Contains($key)) { continue }
            $updated = Get-MPAccountUpdateTime $source.Accounts[$key]
            $old = $null
            if ($scores.TryGetValue($key, [ref]$old)) {
                if ($updated -lt $old.Updated -or ($updated -eq $old.Updated -and $source.Modified -le $old.Modified)) { continue }
            }
            $merged[$key] = $source.Accounts[$key]
            $scores[$key] = [pscustomobject]@{ Updated = $updated; Modified = $source.Modified }
        }
    }

    $needsAccounts = ($merged.Count -gt 0) -and ((-not $hasTarget) -or ($scores.Count -gt 0))
    $needsAssets = $false
    foreach ($source in $sources) {
        foreach ($name in @('gopeed.db', 'task-errors.json', 'refresh_log.json')) {
            if ((Test-Path -LiteralPath (Join-Path $source.Root $name)) -and -not (Test-Path -LiteralPath (Join-Path $dataRoot $name))) {
                $needsAssets = $true
                break
            }
        }
        foreach ($name in @('certs', 'scans', 'account-backups')) {
            if (Test-MPMissingTree (Join-Path $source.Root $name) (Join-Path $dataRoot $name)) {
                $needsAssets = $true
                break
            }
        }
        if ($needsAssets) { break }
    }
    if (-not $needsAccounts -and -not $needsAssets) { return }

    New-Item -ItemType Directory -Path $dataRoot -Force | Out-Null
    $backupRoot = Join-Path $dataRoot 'migration-backups'
    New-Item -ItemType Directory -Path $backupRoot -Force | Out-Null
    $batch = [guid]::NewGuid().ToString('N')
    $index = 0
    foreach ($source in $sources) {
        $index++
        foreach ($name in @('mp.json', 'gopeed.db')) {
            $path = Join-Path $source.Root $name
            if (Test-Path -LiteralPath $path -PathType Leaf) {
                [IO.File]::Copy($path, (Join-Path $backupRoot ($batch + '-source-' + $index + '-' + $name)), $false)
            }
        }
    }

    # The proxy snapshot is session state and must not be carried into a new
    # launch. Other mutable data is copied only when absent. A session's new
    # canonical state always wins over any stale legacy view on later launches.
    foreach ($source in ($sources | Sort-Object Modified -Descending)) {
        foreach ($name in @('gopeed.db', 'task-errors.json', 'refresh_log.json')) {
            $path = Join-Path $source.Root $name
            $target = Join-Path $dataRoot $name
            if ((Test-Path -LiteralPath $path -PathType Leaf) -and -not (Test-Path -LiteralPath $target)) {
                [IO.File]::Copy($path, $target, $false)
            }
        }
        foreach ($name in @('certs', 'scans', 'account-backups')) {
            Copy-MPMissingTree (Join-Path $source.Root $name) (Join-Path $dataRoot $name)
        }
    }

    if ($needsAccounts) {
        if ($PSVersionTable.PSVersion.Major -ge 6) {
            $json = ConvertTo-Json -InputObject $merged -Depth 100 -Compress -ErrorAction Stop
        } else {
            $json = $script:MPJsonSerializer.Serialize($merged)
        }
        $temp = Join-Path $dataRoot ('mp.json.' + [guid]::NewGuid().ToString('N') + '.tmp')
        try {
            [IO.File]::WriteAllText($temp, $json, (New-Object Text.UTF8Encoding($false)))
            if ($hasTarget) {
                [IO.File]::Replace($temp, $accountTarget, (Join-Path $backupRoot ($batch + '-canonical-mp.json')))
            } else {
                [IO.File]::Move($temp, $accountTarget)
            }
        } finally {
            if (Test-Path -LiteralPath $temp) { Remove-Item -LiteralPath $temp -Force }
        }
    }
}
