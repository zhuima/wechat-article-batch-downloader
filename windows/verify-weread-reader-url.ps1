param([Parameter(Mandatory = $true)] [string] $ClientPath)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$assembly = [System.Reflection.Assembly]::LoadFrom([System.IO.Path]::GetFullPath($ClientPath))
$window = $assembly.GetType('WeReadWindow', $true)
$flags = [System.Reflection.BindingFlags]::Static -bor [System.Reflection.BindingFlags]::NonPublic
$method = $window.GetMethod('ReaderURL', $flags)
if (-not $method) { throw 'ReaderURL method is missing.' }

$vectors = @(
    @{ BookID = 'MP_WXS_3631311693'; Hash = '54042c0224d505f5758535f333633313331313639331b8' },
    @{ BookID = 'MP_WXS_3683380834'; Hash = '40142aa224d505f5758535f333638333338303833346b5' }
)
foreach ($vector in $vectors) {
    $actual = [string] $method.Invoke($null, @($vector.BookID))
    $expected = 'https://weread.qq.com/web/mp/reader/' + $vector.Hash
    if ($actual -cne $expected) {
        throw ('ReaderURL mismatch for ' + $vector.BookID + ': ' + $actual)
    }
}
Write-Host ('ReaderURL vectors passed: ' + $vectors.Count)
