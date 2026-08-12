# bindmount-gui.ps1 - WinForms front end for bindmount.exe.
# Requires Windows PowerShell 5.1+ or PowerShell 7 on Windows.

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
[System.Windows.Forms.Application]::EnableVisualStyles()

# WinForms consumes the normal PowerShell pipeline cancellation path. Handle
# Ctrl+C natively and terminate only this GUI host; detached bindmount exec
# processes (and their silos) are deliberately not tracked or touched here.
Add-Type @'
using System;
using System.Diagnostics;

public static class BindmountGuiConsoleCancellation
{
    private static bool installed;

    public static void Install()
    {
        if (installed) return;
        Console.CancelKeyPress += OnCancel;
        installed = true;
    }

    private static void OnCancel(object sender, ConsoleCancelEventArgs args)
    {
        args.Cancel = true;
        Process.GetCurrentProcess().Kill();
    }
}
'@

[BindmountGuiConsoleCancellation]::Install()

$cli = Join-Path $PSScriptRoot 'bindmount.exe'
if (-not (Test-Path -LiteralPath $cli)) {
	$cli = Join-Path $PSScriptRoot '..\dist\bindmount.exe'
}
$decoyExe = Join-Path (Split-Path -Parent $cli) 'decoy.exe'

# Well-known wsl.exe install locations. The "Block WSL" option redirects any
# of these that exist on the host to decoy.exe via file-level bind links.
$script:wslExeCandidates = @(
    (Join-Path ${env:ProgramFiles} 'WSL\wsl.exe'),
    (Join-Path $env:LOCALAPPDATA 'Microsoft\WindowsApps\wsl.exe')
)
$windowsAppsRoot = Join-Path ${env:ProgramFiles} 'WindowsApps'
if (Test-Path -LiteralPath $windowsAppsRoot) {
    $wslPackageDirs = Get-ChildItem -LiteralPath $windowsAppsRoot -Directory -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -like 'MicrosoftCorporationII.WindowsSubsystemForLinux_*' }
    foreach ($packageDir in $wslPackageDirs) {
        $script:wslExeCandidates += (Join-Path $packageDir.FullName 'wsl.exe')
    }
}

function Get-ExistingWslExePaths {
    $seen = @{}
    $paths = @()
    foreach ($candidate in $script:wslExeCandidates) {
        if ($candidate -and (Test-Path -LiteralPath $candidate) -and -not $seen.ContainsKey($candidate.ToLowerInvariant())) {
            $seen[$candidate.ToLowerInvariant()] = $true
            $paths += $candidate
        }
    }
    return $paths
}

function Invoke-Bindmount([string[]]$Arguments) {
    if (-not (Test-Path -LiteralPath $cli)) {
        throw "bindmount.exe was not found: $cli"
    }
    $output = & $cli @Arguments 2>&1 | Out-String
    $code = $LASTEXITCODE
    if ($code -ne 0) {
        throw (($output.Trim()), "bindmount.exe exited with code $code" -join "`r`n")
    }
    return $output.TrimEnd()
}

function Quote-ProcessArgument([string]$Value) {
    if ($Value -notmatch '[\s"]') {
        return $Value
    }
    return '"' + ($Value -replace '(\\*)"', '$1$1\\"' -replace '(\\+)$', '$1$1') + '"'
}

function Start-Bindmount([string[]]$Arguments) {
    if (-not (Test-Path -LiteralPath $cli)) {
        throw "bindmount.exe was not found: $cli"
    }
    $quotedArguments = $Arguments | ForEach-Object { Quote-ProcessArgument $_ }
    $argumentString = $quotedArguments -join ' '
    $process = Start-Process -FilePath $cli -ArgumentList $argumentString `
        -WorkingDirectory (Split-Path -Parent $cli) -WindowStyle Normal -PassThru
    if ($process.WaitForExit(1500)) {
        if ($process.ExitCode -ne 0) {
            throw "The requested process could not be launched. The bindmount launcher reported exit code $($process.ExitCode)."
        }
        return 'Silo process started successfully.'
    }
    return "Started bindmount exec (PID $($process.Id))."
}

function Add-Label($parent, [string]$caption, [int]$x, [int]$y) {
    $label = New-Object Windows.Forms.Label
    $label.Text = $caption
    $label.Location = New-Object Drawing.Point($x, $y)
    $label.AutoSize = $true
    $parent.Controls.Add($label)
}
function Add-TextBox($parent, [int]$x, [int]$y, [int]$width) {
    $box = New-Object Windows.Forms.TextBox
    $box.Location = New-Object Drawing.Point($x, $y)
    $box.Width = $width
    $parent.Controls.Add($box)
    return $box
}
function Add-Button($parent, [string]$caption, [int]$x, [int]$y) {
    $button = New-Object Windows.Forms.Button
    $button.Text = $caption
    $button.Location = New-Object Drawing.Point($x, $y)
    $button.AutoSize = $true
    $parent.Controls.Add($button)
    return $button
}
function Show-Result([string]$value, [bool]$isError = $false) {
    $resultBox.Text = $value
    $resultBox.ForeColor = if ($isError) { [Drawing.Color]::DarkRed } else { [Drawing.Color]::Black }
}
function Run-Action([scriptblock]$action) {
    try {
        Show-Result (& $action)
    } catch {
        Show-Result $_.Exception.Message $true
    }
}

$currentWorkingDirectory = (Get-Location).Path
$gitRepositoryRoot = $null
try {
    $gitRepositoryRoot = (& git -C $currentWorkingDirectory rev-parse --show-toplevel 2>$null | Out-String).Trim()
    if ($gitRepositoryRoot) {
        $gitRepositoryRoot = [IO.Path]::GetFullPath($gitRepositoryRoot)
    } else {
        $gitRepositoryRoot = $null
    }
} catch {
    $gitRepositoryRoot = $null
}

$form = New-Object Windows.Forms.Form
$form.Text = 'bindmount - Bind Filter and Job Silo Manager'
$form.Size = New-Object Drawing.Size(940, 700)
$form.MinimumSize = New-Object Drawing.Size(680, 500)
$form.StartPosition = 'CenterScreen'

$tabs = New-Object Windows.Forms.TabControl
$tabs.Dock = 'Fill'
$listTab = New-Object Windows.Forms.TabPage
$listTab.Text = 'Mappings'
$addTab = New-Object Windows.Forms.TabPage
$addTab.Text = 'Add mapping'
$removeTab = New-Object Windows.Forms.TabPage
$removeTab.Text = 'Remove mapping'
$execTab = New-Object Windows.Forms.TabPage
$execTab.Text = 'Create / run silo'
$siloTab = New-Object Windows.Forms.TabPage
$siloTab.Text = 'Silo management'
$tabs.TabPages.AddRange(@($execTab, $siloTab, $listTab, $addTab, $removeTab))
$form.Controls.Add($tabs)

$resultBox = New-Object Windows.Forms.TextBox
$resultBox.Multiline = $true
$resultBox.ReadOnly = $true
$resultBox.ScrollBars = 'Both'
$resultBox.WordWrap = $false
$resultBox.Font = New-Object Drawing.Font('Consolas', 10)
$resultBox.Dock = 'Bottom'
$resultBox.Height = 100
$form.Controls.Add($resultBox)

# Mappings tab
$listSilo = New-Object Windows.Forms.CheckBox
$listSilo.Text = 'List mappings in silo'
$listSilo.Location = New-Object Drawing.Point(20, 20)
$listSilo.AutoSize = $true
$listTab.Controls.Add($listSilo)
Add-Label $listTab 'Silo name:' 220 22
$listSiloName = Add-TextBox $listTab 290 18 220
Add-Label $listTab 'Volume/path:' 20 58
$listVolume = Add-TextBox $listTab 110 54 400
$listVolume.Text = 'C:\'
$listButton = Add-Button $listTab 'Refresh' 530 52
$listButton.Add_Click({
    Run-Action {
        if ($listSilo.Checked) {
            if (-not $listSiloName.Text) { throw 'Enter a silo name.' }
            Invoke-Bindmount @('list', '--silo', $listSiloName.Text)
        } else {
            Invoke-Bindmount @('list', $listVolume.Text)
        }
    }
})

# Add mapping tab
Add-Label $addTab 'Virtual root:' 20 25
$addVirtual = Add-TextBox $addTab 130 21 600
Add-Label $addTab 'Backing target:' 20 65
$addTarget = Add-TextBox $addTab 130 61 600
$addSiloCheck = New-Object Windows.Forms.CheckBox
$addSiloCheck.Text = 'Scope to silo'
$addSiloCheck.Location = New-Object Drawing.Point(20, 105)
$addSiloCheck.AutoSize = $true
$addTab.Controls.Add($addSiloCheck)
$addSilo = Add-TextBox $addTab 130 101 300
$addReadOnly = New-Object Windows.Forms.CheckBox
$addReadOnly.Text = 'Read-only'
$addReadOnly.Location = New-Object Drawing.Point(450, 103)
$addReadOnly.AutoSize = $true
$addTab.Controls.Add($addReadOnly)
$addMerged = New-Object Windows.Forms.CheckBox
$addMerged.Text = 'Merged'
$addMerged.Location = New-Object Drawing.Point(550, 103)
$addMerged.AutoSize = $true
$addTab.Controls.Add($addMerged)
$addButton = Add-Button $addTab 'Create mapping' 20 145
$addButton.Add_Click({
    Run-Action {
        if (-not $addVirtual.Text -or -not $addTarget.Text) {
            throw 'Enter both a virtual root and backing target.'
        }
        $mapping = if ($addMerged.Checked -and $addReadOnly.Checked) {
            "$($addVirtual.Text)+==$($addTarget.Text)"
        } elseif ($addMerged.Checked) {
            "$($addVirtual.Text)+=$($addTarget.Text)"
        } elseif ($addReadOnly.Checked) {
            "$($addVirtual.Text)==$($addTarget.Text)"
        } else {
            "$($addVirtual.Text)=$($addTarget.Text)"
        }
        $arguments = @('add', $mapping)
        if ($addSiloCheck.Checked) {
            if (-not $addSilo.Text) { throw 'Enter a silo name.' }
            $arguments += @('--silo', $addSilo.Text)
        }
        if ($addSiloCheck.Checked -and $addVirtual.Text.TrimEnd('\') -match '^(?i)C:$') {
            # The silo-launch tab adds C:\Windows by default. Remove that
            # narrower default before installing the broader C:\ mapping;
            # Bind Filter rejects overlapping roots in the opposite order.
            try {
                Invoke-Bindmount @('remove', 'C:\Windows', '--silo', $addSilo.Text) | Out-Null
            } catch {
                # It is fine if the default mapping was not present.
            }
        }
        Invoke-Bindmount $arguments
    }
})

# Remove mapping tab
Add-Label $removeTab 'Virtual root:' 20 25
$removeVirtual = Add-TextBox $removeTab 130 21 600
$removeSiloCheck = New-Object Windows.Forms.CheckBox
$removeSiloCheck.Text = 'Remove from silo'
$removeSiloCheck.Location = New-Object Drawing.Point(20, 65)
$removeSiloCheck.AutoSize = $true
$removeTab.Controls.Add($removeSiloCheck)
$removeSilo = Add-TextBox $removeTab 160 61 300
$removeButton = Add-Button $removeTab 'Remove mapping' 20 105
$removeButton.Add_Click({
    $confirmation = [Windows.Forms.MessageBox]::Show(
        "Remove mapping $($removeVirtual.Text)?", 'Confirm removal', 'YesNo', 'Warning')
    if ($confirmation -ne 'Yes') { return }
    Run-Action {
        if (-not $removeVirtual.Text) { throw 'Enter a virtual root.' }
        $arguments = @('remove', $removeVirtual.Text)
        if ($removeSiloCheck.Checked) {
            if (-not $removeSilo.Text) { throw 'Enter a silo name.' }
            $arguments += @('--silo', $removeSilo.Text)
        }
        Invoke-Bindmount $arguments
    }
})

# Create/run silo tab
Add-Label $execTab 'New silo name:' 20 25
$execName = Add-TextBox $execTab 140 21 300
Add-Label $execTab 'Command:' 20 65
$execCommand = Add-TextBox $execTab 140 61 700
$execCommand.Text = 'pwsh.exe'
Add-Label $execTab 'Arguments (one per line):' 20 105
$execArgs = New-Object Windows.Forms.TextBox
$execArgs.Multiline = $true
$execArgs.ScrollBars = 'Vertical'
$execArgs.Location = New-Object Drawing.Point(180, 101)
$execArgs.Size = New-Object Drawing.Size(660, 60)
$execTab.Controls.Add($execArgs)
Add-Label $execTab 'Links (root=target, root==target read-only, root+=target merged, root+==target both):' 20 175
$execLinks = New-Object Windows.Forms.TextBox
$execLinks.Multiline = $true
$execLinks.ScrollBars = 'Vertical'
$execLinks.Location = New-Object Drawing.Point(20, 200)
$execLinks.Size = New-Object Drawing.Size(820, 70)
$execTab.Controls.Add($execLinks)
$execWindows = New-Object Windows.Forms.CheckBox
$execWindows.Text = 'Map C:\Windows read-write'
$execWindows.Location = New-Object Drawing.Point(20, 250)
$execWindows.AutoSize = $true
$execWindows.Checked = $true
$execTab.Controls.Add($execWindows)
$execRoot = New-Object Windows.Forms.CheckBox
$execRoot.Text = 'Shadow all visible drives (portable root)'
$execRoot.Location = New-Object Drawing.Point(20, 310)
$execRoot.AutoSize = $true
$execTab.Controls.Add($execRoot)
$execRootDir = Add-TextBox $execTab 330 306 510
$execRootDir.Text = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'bindmount\roots'
$execReadOnlyRoot = New-Object Windows.Forms.CheckBox
$execReadOnlyRoot.Text = 'Mark all visible drives read-only'
$execReadOnlyRoot.Location = New-Object Drawing.Point(20, 280)
$execReadOnlyRoot.AutoSize = $true
$execTab.Controls.Add($execReadOnlyRoot)
$execPassthrough = New-Object Windows.Forms.CheckBox
$execPassthrough.Text = 'Passthrough executable directory'
$execPassthrough.Location = New-Object Drawing.Point(20, 335)
$execPassthrough.AutoSize = $true
$execTab.Controls.Add($execPassthrough)
$execPathPassthrough = New-Object Windows.Forms.CheckBox
$execPathPassthrough.Text = 'Passthrough PATH directories'
$execPathPassthrough.Location = New-Object Drawing.Point(360, 335)
$execPathPassthrough.AutoSize = $true
$execTab.Controls.Add($execPathPassthrough)
$execCwdPassthrough = New-Object Windows.Forms.CheckBox
$execCwdPassthrough.Text = 'Passthrough current directory'
$execCwdPassthrough.Location = New-Object Drawing.Point(20, 360)
$execCwdPassthrough.AutoSize = $true
$execTab.Controls.Add($execCwdPassthrough)
$execAppExecPassthrough = New-Object Windows.Forms.CheckBox
$execAppExecPassthrough.Text = 'Passthrough Windows app execution aliases'
$execAppExecPassthrough.Location = New-Object Drawing.Point(360, 360)
$execAppExecPassthrough.AutoSize = $true
$execTab.Controls.Add($execAppExecPassthrough)
$execGitRootPassthrough = New-Object Windows.Forms.CheckBox
$execGitRootPassthrough.Text = 'Passthrough Git repository root'
$execGitRootPassthrough.Location = New-Object Drawing.Point(20, 385)
$execGitRootPassthrough.AutoSize = $true
$execTab.Controls.Add($execGitRootPassthrough)
$execAppStatePassthrough = New-Object Windows.Forms.CheckBox
$execAppStatePassthrough.Text = 'Passthrough APPDATA, LOCALAPPDATA, and C:\ProgramData'
$execAppStatePassthrough.Location = New-Object Drawing.Point(360, 385)
$execAppStatePassthrough.AutoSize = $true
$execTab.Controls.Add($execAppStatePassthrough)
$execRoot.Add_CheckedChanged({
    if ($execRoot.Checked) {
        $execReadOnlyRoot.Checked = $false
        $execPassthrough.Checked = $true
        $execPathPassthrough.Checked = $true
        $execCwdPassthrough.Checked = $true
        $execGitRootPassthrough.Checked = $true
        $execAppExecPassthrough.Checked = $true
        $execBlockWsl.Checked = $true
    }
})
$execReadOnlyRoot.Add_CheckedChanged({
    if ($execReadOnlyRoot.Checked) {
        $execRoot.Checked = $false
        $execBlockWsl.Checked = $true
    }
})
$execBlockWsl = New-Object Windows.Forms.CheckBox
$execBlockWsl.Text = 'Block WSL (redirect wsl.exe to decoy)'
$execBlockWsl.Location = New-Object Drawing.Point(20, 410)
$execBlockWsl.AutoSize = $true
$execTab.Controls.Add($execBlockWsl)
Add-Label $execTab 'Current working directory:' 20 440
$execWorkingDirectory = Add-TextBox $execTab 180 436 660
$execWorkingDirectory.Text = $currentWorkingDirectory
$execWorkingDirectory.ReadOnly = $true
Add-Label $execTab 'Git repository root:' 20 465
$execGitRepositoryRoot = Add-TextBox $execTab 180 461 660
$execGitRepositoryRoot.Text = if ($gitRepositoryRoot) { $gitRepositoryRoot } else { '(none detected)' }
$execGitRepositoryRoot.ReadOnly = $true
# Build the argument list for bindmount exec from the current GUI state.
# Silo name and command validity are checked by the callers that need them.
function Get-ExecArguments {
    param([switch]$Detach)
    $arguments = @('exec')
    if ($Detach) { $arguments += '--detach' }
    if ($execRoot.Checked -and $execReadOnlyRoot.Checked) {
        throw 'Shadow all visible drives and Mark all visible drives read-only are mutually exclusive.'
    }
    if ($execRoot.Checked) {
        if (-not $execRootDir.Text) { throw 'Enter a root backing directory.' }
        $arguments += @('--root', $execRootDir.Text)
    }
    if ($execReadOnlyRoot.Checked) {
        $arguments += '--readonly-root'
    }
    if ($execPassthrough.Checked) {
        $arguments += @('--passthrough', 'executable')
    } elseif ($execRoot.Checked) {
        $arguments += @('--no-passthrough', 'executable')
    }
    foreach ($option in @(
        @{ Checked = $execPathPassthrough.Checked; Name = 'path' },
        @{ Checked = $execCwdPassthrough.Checked; Name = 'cwd' },
        @{ Checked = $execGitRootPassthrough.Checked; Name = 'gitroot' },
        @{ Checked = $execAppExecPassthrough.Checked; Name = 'appexec' },
        @{ Checked = $execAppStatePassthrough.Checked; Name = 'appstate' }
    )) {
        if ($option.Checked) {
            $arguments += @('--passthrough', $option.Name)
        } elseif ($execRoot.Checked) {
            $arguments += @('--no-passthrough', $option.Name)
        }
    }
    $hasWindowsRoot = $false
    foreach ($line in ($execLinks.Lines | Where-Object { $_.Trim() })) {
        $link = $line.Trim()
        $arguments += @('--link', $link)
        if ($link -match '(?i)^C:\\(?:Windows(?:\\|=)|=)') {
            $hasWindowsRoot = $true
        }
    }
    if ($execBlockWsl.Checked) {
        # Block WSL launches inside the silo by redirecting every wsl.exe
        # found on the host to the decoy, which prints an explanatory error
        # instead of launching WSL. Links are file-level so wsl.exe is
        # shadowed even when its parent directory is passed through.
        # Execution aliases under WindowsApps are 0-byte APPEXECLINK reparse
        # points the driver cannot map; bindmount renames those aside itself
        # (wsl.exe.bindmount-blocked), keeping the rename in --detach mode so
        # the block persists. Already-blocked aliases are skipped.
        $wslPaths = Get-ExistingWslExePaths
        if ($wslPaths.Count -gt 0) {
            if (-not (Test-Path -LiteralPath $decoyExe)) {
                throw "Block WSL requires decoy.exe next to bindmount.exe: $decoyExe"
            }
            foreach ($wslPath in $wslPaths) {
                $item = Get-Item -LiteralPath $wslPath -Force
                if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -and
                    (Test-Path -LiteralPath "$wslPath.bindmount-blocked")) {
                    continue
                }
                $arguments += @('--link', "$wslPath==$decoyExe")
            }
        }
    }
    if ($execWindows.Checked -and -not $hasWindowsRoot) {
        # Install user links first. A broader C:\ mapping must precede
        # C:\Windows; if the user supplied C:\ itself, omit this default
        # because the broader mapping owns that subtree.
        $arguments += @('--link', 'C:\Windows=C:\Windows')
    }
    $arguments += @($execName.Text, '--', $execCommand.Text)
    $arguments += @($execArgs.Lines | Where-Object { $_.Trim() })
    return ,$arguments
}

$execButton = Add-Button $execTab 'Create silo and run command' 20 495
$execButton.Add_Click({
    Run-Action {
        if (-not $execName.Text -or -not $execCommand.Text) {
            throw 'Enter a silo name and command.'
        }
        # bindmount itself renames app execution aliases aside (the driver
        # cannot anchor a mapping on them), keeping the rename in --detach
        # mode so the block persists. Nothing extra to do here.
        Start-Bindmount (Get-ExecArguments -Detach)
    }
})
$execCopyButton = Add-Button $execTab 'Copy command' 250 495
$execCopyButton.Add_Click({
    Run-Action {
        $arguments = Get-ExecArguments
        $commandLine = "`"$cli`" " + (($arguments | ForEach-Object { Quote-ProcessArgument $_ }) -join ' ')
        [Windows.Forms.Clipboard]::SetText($commandLine)
        "Copied to clipboard:`r`n$commandLine"
    }
})

# Silo management tab
Add-Label $siloTab 'Silo name:' 20 25
$managedSilo = Add-TextBox $siloTab 110 21 350
$siloExistsButton = Add-Button $siloTab 'Check exists' 20 65
$siloKillButton = Add-Button $siloTab 'Kill entire silo' 130 65
$siloExistsButton.Add_Click({
    Run-Action {
        if (-not $managedSilo.Text) { throw 'Enter a silo name.' }
        Invoke-Bindmount @('silo', 'exists', $managedSilo.Text)
    }
})
$siloKillButton.Add_Click({
    $confirmation = [Windows.Forms.MessageBox]::Show(
        "Terminate every process in silo $($managedSilo.Text)?", 'Confirm silo termination', 'YesNo', 'Warning')
    if ($confirmation -ne 'Yes') { return }
    Run-Action {
        if (-not $managedSilo.Text) { throw 'Enter a silo name.' }
        Invoke-Bindmount @('silo', 'kill', $managedSilo.Text)
    }
})

$form.Add_Shown({ $listButton.PerformClick() })
[void]$form.ShowDialog()
