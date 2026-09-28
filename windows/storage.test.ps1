$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'storage.ps1')

function Assert([bool] $condition, [string] $message) {
    if (-not $condition) { throw $message }
}

$tempBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
$fixture = [IO.Path]::GetFullPath((Join-Path $tempBase ('mp-storage-test-' + [guid]::NewGuid().ToString('N'))))
Assert ($fixture.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase)) 'Invalid test path.'
$physical = Join-Path $fixture 'physical'
$virtual = Join-Path $fixture 'virtual'
$canonical = Join-Path $fixture 'canonical'
try {
    New-Item -ItemType Directory -Path $physical, $virtual | Out-Null
    $parserFixture = Join-Path $fixture 'parser-valid.json'
    [IO.File]::WriteAllText($parserFixture, '{"Case":{"update_time":1,"nickname":"测试"},"case":{"update_time":2,"nickname":"second"}}')
    $parsed = Read-MPAccounts $parserFixture
    Assert ($parsed.Count -eq 2 -and $parsed['Case']['nickname'] -eq '测试' -and $parsed['case']['nickname'] -eq 'second') 'Valid account JSON or case-sensitive IDs could not be parsed.'

    [IO.File]::WriteAllText((Join-Path $physical 'mp.json'), '{"same":{"update_time":1,"cookie":"older"},"physical":{"update_time":3,"cookie":"keep"}}')
    [IO.File]::WriteAllText((Join-Path $virtual 'mp.json'), '{"same":{"update_time":2,"cookie":"newer"},"virtual":{"update_time":4,"cookie":"keep"},"Case":{"update_time":1},"case":{"update_time":1}}')
    [IO.File]::WriteAllText((Join-Path $physical 'gopeed.db'), 'older')
    [IO.File]::WriteAllText((Join-Path $virtual 'gopeed.db'), 'newer')
    New-Item -ItemType Directory -Path (Join-Path $physical 'scans'), (Join-Path $virtual 'scans') | Out-Null
    [IO.File]::WriteAllText((Join-Path $physical 'scans\physical.json'), '{}')
    [IO.File]::WriteAllText((Join-Path $virtual 'scans\virtual.json'), '{}')
    $sourceHashes = @((Get-FileHash (Join-Path $physical 'mp.json')).Hash, (Get-FileHash (Join-Path $virtual 'mp.json')).Hash)

    Merge-MPStorage $canonical @($physical, $virtual)
    $merged = Read-MPAccounts (Join-Path $canonical 'mp.json')
    Assert ($merged.Count -eq 5) 'Account keys were not combined or case-sensitive IDs were lost.'
    Assert ($merged['same']['cookie'] -eq 'newer') 'Newer source credentials did not win.'
    Assert ($merged['physical']['cookie'] -eq 'keep' -and $merged['virtual']['cookie'] -eq 'keep') 'A source account was lost.'
    Assert ((Test-Path (Join-Path $canonical 'scans\physical.json')) -and (Test-Path (Join-Path $canonical 'scans\virtual.json'))) 'Scan states were not combined.'
    Assert (@(Get-ChildItem (Join-Path $canonical 'migration-backups') -Filter '*-source-*-mp.json').Count -eq 2) 'Both source account files were not backed up.'
    Assert (((Get-FileHash (Join-Path $physical 'mp.json')).Hash -eq $sourceHashes[0]) -and ((Get-FileHash (Join-Path $virtual 'mp.json')).Hash -eq $sourceHashes[1])) 'A source changed.'

    $backupCount = @(Get-ChildItem (Join-Path $canonical 'migration-backups') -File).Count
    Merge-MPStorage $canonical @($physical, $virtual)
    Assert (@(Get-ChildItem (Join-Path $canonical 'migration-backups') -File).Count -eq $backupCount) 'An idempotent run created more backups.'

    [IO.File]::WriteAllText((Join-Path $physical 'scans\late.json'), '{}')
    Merge-MPStorage $canonical @($physical, $virtual)
    Assert (Test-Path (Join-Path $canonical 'scans\late.json')) 'A later scan state was not imported.'

    [IO.File]::WriteAllText((Join-Path $canonical 'mp.json'), '{"same":{"update_time":20,"cookie":"canonical"},"physical":{"update_time":3,"cookie":"keep"},"virtual":{"update_time":4,"cookie":"keep"}}')
    [IO.File]::WriteAllText((Join-Path $physical 'mp.json'), '{"same":{"update_time":1,"cookie":"older"},"physical":{"update_time":3,"cookie":"keep"},"late":{"update_time":5,"cookie":"older"}}')
    [IO.File]::WriteAllText((Join-Path $virtual 'mp.json'), '{"same":{"update_time":2,"cookie":"newer"},"virtual":{"update_time":4,"cookie":"keep"},"late":{"update_time":6,"cookie":"added"}}')
    Merge-MPStorage $canonical @($physical, $virtual)
    $merged = Read-MPAccounts (Join-Path $canonical 'mp.json')
    Assert ($merged['same']['cookie'] -eq 'canonical' -and $merged['late']['cookie'] -eq 'added') 'A later import overwrote canonical credentials or missed a new account.'
    Assert (@(Get-ChildItem (Join-Path $canonical 'migration-backups') -Filter '*-canonical-mp.json').Count -eq 1) 'Canonical account file was not backed up before replacement.'
    Write-Output 'Windows storage migration fixtures passed.'
} finally {
    if ($fixture.StartsWith($tempBase, [StringComparison]::OrdinalIgnoreCase) -and (Test-Path -LiteralPath $fixture)) {
        Remove-Item -LiteralPath $fixture -Recurse -Force
    }
}
