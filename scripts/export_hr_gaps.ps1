param(
    [string]$CsvPath = (Join-Path (Split-Path -Parent $PSScriptRoot) 'data/employees.csv'),
    [string]$OutputPath = (Join-Path (Split-Path -Parent $PSScriptRoot) 'data/hr_gap_report.csv')
)

$rows = Import-Csv -LiteralPath $CsvPath
$duplicateNames = @{}
foreach ($group in $rows | Where-Object { $_.employment_status -notmatch '^(inactive|former|left|resigned|terminated|retired|separated)$' } | Group-Object { ([string]$_.name -replace '\s+', ' ').Trim().ToLowerInvariant() }) {
    if ($group.Count -gt 1 -and $group.Name -ne '') { $duplicateNames[$group.Name] = $true }
}
$duplicateIds = @{}
foreach ($group in $rows | Where-Object { $_.hr_id } | Group-Object { ([string]$_.hr_id).Trim().ToLowerInvariant() }) {
    if ($group.Count -gt 1) { $duplicateIds[$group.Name] = $true }
}
$validStatuses = @('active', 'current', 'employed', 'inactive', 'former', 'left', 'resigned', 'terminated', 'retired', 'separated')
$fields = @(
    @{ Name = 'designation'; Label = 'designation' },
    @{ Name = 'department'; Label = 'department' },
    @{ Name = 'employment_status'; Label = 'employment status' },
    @{ Name = 'employment_type'; Label = 'employment type' },
    @{ Name = 'extension'; Label = 'phone / extension' },
    @{ Name = 'blood_group'; Label = 'blood group' },
    @{ Name = 'joining_date'; Label = 'joining date' },
    @{ Name = 'age'; Label = 'age' },
    @{ Name = 'service_length'; Label = 'service length' },
    @{ Name = 'personal_email'; Label = 'personal email' },
    @{ Name = 'whatsapp_number'; Label = 'WhatsApp number' },
    @{ Name = 'record_updated_at'; Label = 'HR record updated date' }
)

$report = foreach ($row in $rows) {
    if ($row.employment_status -match '^(inactive|former|left|resigned|terminated|retired|separated)$') {
        continue
    }
    $missing = @(
      foreach ($field in $fields) {
        $property = $row.PSObject.Properties[$field.Name]
        if ($null -eq $property -or [string]::IsNullOrWhiteSpace([string]$property.Value)) {
            $field.Label
        }
      }
    )
    $review = @()
    $status = ([string]$row.employment_status).Trim().ToLowerInvariant()
    if ($status -and $status -notin $validStatuses) { $review += "nonstandard status: $($row.employment_status)" }
    $nameKey = ([string]$row.name -replace '\s+', ' ').Trim().ToLowerInvariant()
    if ($duplicateNames.ContainsKey($nameKey)) { $review += 'duplicate name; verify identity' }
    $idKey = ([string]$row.hr_id).Trim().ToLowerInvariant()
    if ($idKey -and $duplicateIds.ContainsKey($idKey)) { $review += 'duplicate HR ID; verify source row' }
    if ($row.joining_date) {
        $parsedDate = [DateTime]::MinValue
        if (![DateTime]::TryParse([string]$row.joining_date, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::None, [ref]$parsedDate)) {
            $review += 'invalid joining date; verify source value'
        }
    }
    if ($missing.Count -gt 0 -or $review.Count -gt 0) {
        [PSCustomObject]@{
            employee_id   = $row.hr_id
            name          = $row.name
            department    = $row.department
            missing_fields = ($missing -join '; ')
            review_flags   = ($review -join '; ')
        }
    }
}

$report | Export-Csv -LiteralPath $OutputPath -NoTypeInformation -Encoding utf8
Write-Output "Wrote $(@($report).Count) non-inactive employee rows with missing fields to $OutputPath"
