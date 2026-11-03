param(
    [Parameter(Mandatory = $true)][string]$Workbook,
    [Parameter(Mandatory = $true)][string]$CsvPath,
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression.FileSystem

function Read-ZipEntry([System.IO.Compression.ZipArchive]$Archive, [string]$Name) {
    $entry = $Archive.GetEntry($Name)
    if ($null -eq $entry) { throw "Workbook entry not found: $Name" }
    $reader = [System.IO.StreamReader]::new($entry.Open())
    try { return $reader.ReadToEnd() } finally { $reader.Dispose() }
}

function Normalize-Header([string]$Value) {
    return (($Value -replace '[^A-Za-z0-9]', '').ToLowerInvariant())
}

function Clean-Value([string]$Value) {
    $value = ($Value -replace '[\r\n\t]+', ' ').Trim()
    if ($value -match '^(?i)(-|#N/A|N/A|NA|NULL|NOT LISTED)$') { return '' }
    return $value
}

function Get-Field([hashtable]$Values, [string[]]$Names) {
    foreach ($name in $Names) {
        $key = Normalize-Header $name
        if ($Values.ContainsKey($key)) {
            $value = Clean-Value ([string]$Values[$key])
            if ($value -ne '') { return $value }
        }
    }
    return ''
}

function Normalize-ExcelDate([string]$Value) {
    $value = Clean-Value $Value
    if ($value -eq '') { return '' }
    $serial = 0.0
    if ([double]::TryParse($value, [Globalization.NumberStyles]::Float, [Globalization.CultureInfo]::InvariantCulture, [ref]$serial) -and $serial -gt 20000 -and $serial -lt 80000) {
        return [DateTime]::FromOADate($serial).ToString('yyyy-MM-dd', [Globalization.CultureInfo]::InvariantCulture)
    }
    $date = [DateTime]::MinValue
    if ([DateTime]::TryParse($value, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::AssumeLocal, [ref]$date)) {
        return $date.ToString('yyyy-MM-dd', [Globalization.CultureInfo]::InvariantCulture)
    }
    return $value
}

function Normalize-Period([string]$Value) {
    $value = (Clean-Value $Value).Trim('"', "'")
    if ($value -match '(?i)^\s*(\d+)\s*[- ]?y\w*[, ]+\s*(\d+)\s*[- ]?m\w*[, ]+\s*(\d+)\s*[- ]?d\w*') {
        return "$($Matches[1]) years, $($Matches[2]) months, $($Matches[3]) days"
    }
    return $value
}

function Normalize-Status([string]$Value) {
    $value = Clean-Value $Value
    if ($value -match '(?i)in[ -]*active|former|resign|retir|terminat|separat|left') { return 'inactive' }
    if ($value -eq '' -or $value -match '(?i)^active($|\s*-)|^DC$') { return 'active' }
    return $value.ToLowerInvariant()
}

function Normalize-EmploymentType([string]$Value) {
    $value = Clean-Value $Value
    if ($value -match '(?i)^regular\b') { return 'Permanent' }
    if ($value -match '(?i)part[ -]*timer') { return 'Part-Time' }
    if ($value -match '(?i)\bintern(ship)?\b') { return 'Intern' }
    if ($value -match '(?i)contract') { return 'Contractual' }
    return $value
}

function Normalize-WhatsAppNumber([string]$Value) {
    $pattern = '(?<![0-9])(?:\+?88|0088)?[\s().-]*0?1[3-9](?:[\s().-]*[0-9]){8}(?![0-9])'
    foreach ($match in [regex]::Matches((Clean-Value $Value), $pattern)) {
        $digits = $match.Value -replace '[^0-9]', ''
        if ($digits.StartsWith('0088')) { $digits = $digits.Substring(4) }
        elseif ($digits.StartsWith('88')) { $digits = $digits.Substring(2) }
        if ($digits.StartsWith('0')) { $digits = $digits.Substring(1) }
        if ($digits -match '^1[3-9][0-9]{8}$') { return '880' + $digits }
    }
    return ''
}

function Read-WorkbookRows([System.IO.Compression.ZipArchive]$Archive, [string]$SharedStringsPath = 'xl/sharedStrings.xml') {
    [xml]$book = Read-ZipEntry $Archive 'xl/workbook.xml'
    [xml]$relationships = Read-ZipEntry $Archive 'xl/_rels/workbook.xml.rels'
    $bookNs = [System.Xml.XmlNamespaceManager]::new($book.NameTable)
    $bookNs.AddNamespace('m', 'http://schemas.openxmlformats.org/spreadsheetml/2006/main')
    $bookNs.AddNamespace('r', 'http://schemas.openxmlformats.org/officeDocument/2006/relationships')
    $relationshipNs = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'
    $targets = @{}
    foreach ($relationship in $relationships.Relationships.Relationship) { $targets[$relationship.Id] = $relationship.Target }

    $shared = @()
    if ($Archive.GetEntry($SharedStringsPath)) {
        [xml]$stringDoc = Read-ZipEntry $Archive $SharedStringsPath
        foreach ($item in $stringDoc.sst.si) { $shared += ,$item.InnerText }
    }

    $all = @()
    foreach ($sheet in $book.SelectNodes('//m:sheets/m:sheet', $bookNs)) {
        $sheetName = [string]$sheet.name
        $target = [string]$targets[$sheet.GetAttribute('id', $relationshipNs)]
        $entryName = ('xl/' + $target.TrimStart('/')).Replace('xl/xl/', 'xl/')
        [xml]$doc = Read-ZipEntry $Archive $entryName
        $sheetNs = [System.Xml.XmlNamespaceManager]::new($doc.NameTable)
        $sheetNs.AddNamespace('m', 'http://schemas.openxmlformats.org/spreadsheetml/2006/main')
        $rows = $doc.SelectNodes('//m:sheetData/m:row', $sheetNs)
        $headerRow = $null
        $headerMap = @{}
        foreach ($row in $rows) {
            $candidate = @{}
            foreach ($cell in $row.SelectNodes('m:c', $sheetNs)) {
                $valueNode = $cell.SelectSingleNode('m:v', $sheetNs)
                $value = ''
                if ($null -ne $valueNode) {
                    $value = $valueNode.InnerText
                    if ($cell.GetAttribute('t') -eq 's') { $value = $shared[[int]$value] }
                } elseif ($cell.GetAttribute('t') -eq 'inlineStr') {
                    $value = $cell.SelectSingleNode('m:is', $sheetNs).InnerText
                }
                $column = [regex]::Replace($cell.GetAttribute('r'), '[0-9]', '')
                $candidate[$column] = (Clean-Value $value)
            }
            if (@($candidate.Values | ForEach-Object { Normalize-Header $_ }) -contains 'hrid') {
                $headerRow = $row
                foreach ($column in $candidate.Keys) { $headerMap[$column] = Normalize-Header $candidate[$column] }
                break
            }
        }
        if ($null -eq $headerRow) { continue }

        foreach ($row in $rows) {
            if ([int]$row.r -le [int]$headerRow.r) { continue }
            $values = @{}
            foreach ($cell in $row.SelectNodes('m:c', $sheetNs)) {
                $valueNode = $cell.SelectSingleNode('m:v', $sheetNs)
                $value = ''
                if ($null -ne $valueNode) {
                    $value = $valueNode.InnerText
                    if ($cell.GetAttribute('t') -eq 's') { $value = $shared[[int]$value] }
                } elseif ($cell.GetAttribute('t') -eq 'inlineStr') {
                    $value = $cell.SelectSingleNode('m:is', $sheetNs).InnerText
                }
                $column = [regex]::Replace($cell.GetAttribute('r'), '[0-9]', '')
                $key = [string]$headerMap[$column]
                if ($key) { $values[$key] = Clean-Value $value }
            }
            $id = Get-Field $values @('HR ID')
            $name = Get-Field $values @('Name')
            if ($id -eq '' -and $name -eq '') { continue }
            $all += [pscustomobject]@{ Sheet = $sheetName; Values = $values; HRID = $id; Name = $name }
        }
    }
    return ,$all
}

function Normalize-NameKey([string]$Name) {
    if ($null -eq $Name) { $Name = '' }
    return [regex]::Replace(($Name.ToLowerInvariant()), '[^a-z0-9]+', ' ').Trim()
}

if (!(Test-Path -LiteralPath $Workbook)) { throw "Workbook not found: $Workbook" }
if (!(Test-Path -LiteralPath $CsvPath)) { throw "CSV not found: $CsvPath" }

$archive = [System.IO.Compression.ZipFile]::OpenRead((Resolve-Path -LiteralPath $Workbook).Path)
try { $sourceRows = Read-WorkbookRows $archive } finally { $archive.Dispose() }

$bloodGroups = @{}
foreach ($source in $sourceRows | Where-Object { $_.Sheet -notin @('Master File', 'DC', 'InternPart Timer') }) {
    # This workbook's supporting Sheet14 tab is titled "Blood Group List (A+)".
    if ($source.HRID -ne '' -and $source.Sheet -eq 'Sheet14') { $bloodGroups[$source.HRID.ToLowerInvariant()] = 'A+' }
}

$incoming = @{}
$sourceConflicts = @()
$sourceEmployeeCount = 0
foreach ($source in $sourceRows | Where-Object { $_.Sheet -in @('Master File', 'DC', 'InternPart Timer') }) {
    if ($source.Name -eq '') { continue }
    $sourceEmployeeCount++
    $values = $source.Values
    $status = Get-Field $values @('Status', 'Active/Inactive')
    $employmentStatus = Normalize-Status $status
    $employmentType = Normalize-EmploymentType (Get-Field $values @('Employment Status'))
    $department = Get-Field $values @('Department')
    if ($source.Sheet -eq 'DC' -and $department -eq '') { $department = 'District Correspondent' }
    $officialEmail = Get-Field $values @('DS Mail')
    $personalEmail = Get-Field $values @('Personal Email')
    if ($source.Sheet -eq 'DC') {
        $officialIsOnDailyStar = $officialEmail -match '(?i)@thedailystar\.net$'
        $personalIsOnDailyStar = $personalEmail -match '(?i)@thedailystar\.net$'
        if (!$officialIsOnDailyStar -and $personalIsOnDailyStar) { $swap = $officialEmail; $officialEmail = $personalEmail; $personalEmail = $swap }
    }
    $officialEmail = $officialEmail -replace '\s*;\s*$', ''
    $personalEmail = $personalEmail -replace '\s*;\s*$', ''
    $floor = Get-Field $values @('Floor')
    if ($floor -match '^\d+$') {
        $number = [int]$floor
        $suffix = if ($number % 100 -in 11,12,13) { 'th' } else { switch ($number % 10) { 1 {'st'} 2 {'nd'} 3 {'rd'} default {'th'} } }
        $floor = "$number$suffix Floor"
    } elseif ($floor -and $floor -notmatch '(?i)floor$') { $floor = "$floor Floor" }

    $employee = [ordered]@{
        hr_id = $source.HRID
        name = $source.Name
        designation = Get-Field $values @('Designation')
        department = $department
        extension = "ID: $($source.HRID)"
        email = $officialEmail
        desk = $floor
        joining_date = Normalize-ExcelDate (Get-Field $values @('Joining Date'))
        blood_group = Get-Field $values @('Blood Group')
        age = Normalize-Period (Get-Field $values @('Age'))
        service_length = Normalize-Period (Get-Field $values @('Service Length'))
        personal_email = $personalEmail
        whatsapp_number = Normalize-WhatsAppNumber (Get-Field $values @('Contact No.', 'Mobile', 'Phone'))
        name_aliases = ''
        employment_type = $employmentType
        employment_status = $employmentStatus
    }
    if ($employee.blood_group -eq '' -and $source.HRID -ne '' -and $bloodGroups.ContainsKey($source.HRID.ToLowerInvariant())) { $employee.blood_group = $bloodGroups[$source.HRID.ToLowerInvariant()] }

    $key = if ($source.HRID -ne '') { 'id:' + $source.HRID.ToLowerInvariant() } else { 'name:' + (Normalize-NameKey $source.Name) }
    if (!$incoming.ContainsKey($key)) {
        $incoming[$key] = $employee
    } else {
        $previous = $incoming[$key]
        if ((Normalize-NameKey $previous.name) -ne (Normalize-NameKey $employee.name)) {
            $sourceConflicts += "$($source.HRID): '$($previous.name)' / '$($employee.name)'"
            $aliases = @(([string]$previous.name_aliases -split '\|' | Where-Object { $_ -ne '' }) + $employee.name)
            $previous.name_aliases = (($aliases | Select-Object -Unique) -join '|')
            if ($previous.employment_status -eq 'inactive' -and $employee.employment_status -eq 'active') {
                $keptAliases = $previous.name_aliases
                foreach ($field in $employee.Keys) { $previous[$field] = $employee[$field] }
                $previous.name_aliases = $keptAliases
            }
        }
        foreach ($field in $employee.Keys) {
            if ([string]$previous[$field] -eq '' -and [string]$employee[$field] -ne '') { $previous[$field] = $employee[$field] }
        }
    }
}

$existing = @(Import-Csv -LiteralPath $CsvPath)
if ($existing.Count -eq 0) { throw 'The CSV contains no employee rows.' }
$csvHeaders = @($existing[0].PSObject.Properties.Name)
foreach ($field in @('hr_id', 'age', 'service_length', 'personal_email', 'employment_type', 'whatsapp_number')) {
    if ($csvHeaders -notcontains $field) { $csvHeaders += $field }
}
foreach ($row in $existing) {
    foreach ($field in $csvHeaders) {
        if ($null -eq $row.PSObject.Properties[$field]) { $row | Add-Member -NotePropertyName $field -NotePropertyValue '' }
    }
    if (!$row.hr_id) {
        foreach ($property in $row.PSObject.Properties) {
            $idMatch = [regex]::Match([string]$property.Value, '(?i)ID:\s*((?:[A-Za-z]+-)?[0-9]+)\b')
            if ($idMatch.Success) { $row.hr_id = $idMatch.Groups[1].Value; break }
        }
    }
}

# Previous CSV exports sometimes shifted a phone/HR-ID cell into another
# column. Recover IDs from every field above, then merge duplicate rows by the
# exact HR ID while retaining non-empty fields and all observed name variants.
$deduplicated = [System.Collections.Generic.List[object]]::new()
$existingById = @{}
$duplicateRowsMerged = 0
foreach ($row in $existing) {
    $idKey = ([string]$row.hr_id).ToLowerInvariant()
    if ($idKey -ne '' -and $existingById.ContainsKey($idKey)) {
        $target = $existingById[$idKey]
        if ((Normalize-NameKey $target.name) -ne (Normalize-NameKey $row.name) -and $row.name) {
            $aliases = @(([string]$target.name_aliases -split '\|' | Where-Object { $_ -ne '' }) + [string]$row.name)
            $target.name_aliases = (($aliases | Select-Object -Unique) -join '|')
        }
        foreach ($field in $csvHeaders) {
            if ([string]$target.$field -eq '' -and [string]$row.$field -ne '') { $target.$field = [string]$row.$field }
        }
        if ($target.employment_status -eq 'inactive' -and $row.employment_status -eq 'active') { $target.employment_status = 'active' }
        if ($target.extension -notmatch '(?i)ID:\s*((?:[A-Za-z]+-)?[0-9]+)\b' -and $row.extension -match '(?i)ID:\s*((?:[A-Za-z]+-)?[0-9]+)\b') { $target.extension = $row.extension }
        $duplicateRowsMerged++
        continue
    }
    $deduplicated.Add($row)
    if ($idKey -ne '') { $existingById[$idKey] = $row }
}
$existing = @($deduplicated.ToArray())

$byId = @{}
$byName = @{}
foreach ($row in $existing) {
    if ($row.hr_id) { $byId[$row.hr_id.ToLowerInvariant()] = $row }
    $nameKey = Normalize-NameKey $row.name
    if ($nameKey -and !$byName.ContainsKey($nameKey)) { $byName[$nameKey] = $row }
}

$added = 0
$updated = 0
foreach ($key in $incoming.Keys) {
    $source = $incoming[$key]
    $target = $null
    if ($source.hr_id -and $byId.ContainsKey($source.hr_id.ToLowerInvariant())) { $target = $byId[$source.hr_id.ToLowerInvariant()] }
    elseif (!$source.hr_id -and $byName.ContainsKey((Normalize-NameKey $source.name))) { $target = $byName[(Normalize-NameKey $source.name)] }
    if ($null -eq $target) {
        $target = [pscustomobject]@{}
        foreach ($field in $csvHeaders) { $target | Add-Member -NotePropertyName $field -NotePropertyValue '' }
        $target.name = $source.name
        $target.extension = $source.extension
        $existing += $target
        $added++
    } else {
        $updated++
        if ((Normalize-NameKey $target.name) -ne (Normalize-NameKey $source.name)) {
            $oldNameAliases = @(([string]$source.name_aliases -split '\|' | Where-Object { $_ -ne '' }) + [string]$target.name)
            $source.name_aliases = (($oldNameAliases | Select-Object -Unique) -join '|')
        }
    }

    foreach ($field in @('name', 'designation', 'department', 'email', 'desk', 'joining_date', 'blood_group', 'age', 'service_length', 'personal_email', 'employment_status', 'employment_type', 'whatsapp_number')) {
        if ([string]$source[$field] -ne '') { $target.$field = [string]$source[$field] }
    }
    if ($source.name_aliases -ne '') {
        $allAliases = @(([string]$target.name_aliases -split '\|' | Where-Object { $_ -ne '' }) + ([string]$source.name_aliases -split '\|' | Where-Object { $_ -ne '' }))
        $target.name_aliases = (($allAliases | Select-Object -Unique) -join '|')
    }
    if ($source.hr_id -ne '') {
        $target.hr_id = $source.hr_id
        if ($target.extension -notmatch '(?i)ID:\s*((?:[A-Za-z]+-)?[0-9]+)\b') { $target.extension = $source.extension }
        $byId[$source.hr_id.ToLowerInvariant()] = $target
    }
    $byName[(Normalize-NameKey $source.name)] = $target
}

$active = @($existing | Where-Object { $_.employment_status -eq 'active' }).Count
$inactive = @($existing | Where-Object { $_.employment_status -eq 'inactive' }).Count
$permanent = @($existing | Where-Object { $_.employment_type -eq 'Permanent' }).Count
$partTime = @($existing | Where-Object { $_.employment_type -eq 'Part-Time' }).Count
$whatsAppEnabled = @($existing | Where-Object { $_.whatsapp_number }).Count
"Workbook employee rows with names: $sourceEmployeeCount"
"Unique workbook IDs/names imported: $($incoming.Count)"
"Existing records updated: $updated; new records added: $added"
"Duplicate CSV rows merged by HR ID: $duplicateRowsMerged"
"CSV total after merge: $($existing.Count); active: $active; inactive: $inactive"
"Employment types: permanent=$permanent; part-time=$partTime"
"Records with a verified mobile for WhatsApp: $whatsAppEnabled"
"Extra A+ blood-group matches available: $($bloodGroups.Count)"
if ($sourceConflicts.Count -gt 0) { "Source alternate-name spellings for duplicate HR IDs: $($sourceConflicts -join '; ') (saved as search aliases)" }
if ($DryRun) { 'Dry run: CSV was not changed.'; exit 0 }

$output = $existing | Select-Object $csvHeaders | ConvertTo-Csv -NoTypeInformation
[System.IO.File]::WriteAllLines((Resolve-Path -LiteralPath $CsvPath).Path, $output, [System.Text.UTF8Encoding]::new($false))
"Updated CSV: $CsvPath"
