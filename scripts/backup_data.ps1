param(
    [Parameter(Mandatory = $true)][string]$BackupDirectory
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path.TrimEnd('\')
$destination = [System.IO.Path]::GetFullPath($BackupDirectory).TrimEnd('\')
if ($destination.Equals($repoRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
    $destination.StartsWith($repoRoot + '\', [System.StringComparison]::OrdinalIgnoreCase)) {
    throw 'Choose a protected backup directory outside the project workspace.'
}

New-Item -ItemType Directory -Path $destination -Force | Out-Null
$dataDir = Join-Path $repoRoot 'data'
$paths = @(
    (Join-Path $dataDir 'employees.csv'),
    (Join-Path $dataDir 'employees_index.json'),
    (Join-Path $dataDir 'knowledge_index.json'),
    (Join-Path $dataDir 'knowledge')
)
$paths += Join-Path $dataDir 'feedback.jsonl'
$paths += Join-Path $dataDir 'hr_gap_report.csv'
$existing = @($paths | Where-Object { Test-Path -LiteralPath $_ })
if ($existing.Count -eq 0) { throw 'No chatbot data files were found to back up.' }

$stamp = [DateTime]::UtcNow.ToString('yyyyMMdd-HHmmssZ')
$archive = Join-Path $destination "dailystar-chatbot-data-$stamp.zip"
Compress-Archive -LiteralPath $existing -DestinationPath $archive -CompressionLevel Optimal
Write-Output "Created protected data backup: $archive"
Write-Output 'Store it encrypted, apply the HR retention policy, and periodically verify restoration.'
