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

$form = New-Object Windows.Forms.Form
$form.Text = 'bindmount - Bind Filter and Job Silo Manager'
$form.Size = New-Object Drawing.Size(980, 720)
$form.MinimumSize = New-Object Drawing.Size(700, 500)
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
$tabs.TabPages.AddRange(@($listTab, $addTab, $removeTab, $execTab, $siloTab))
$form.Controls.Add($tabs)

$resultBox = New-Object Windows.Forms.TextBox
$resultBox.Multiline = $true
$resultBox.ReadOnly = $true
$resultBox.ScrollBars = 'Both'
$resultBox.WordWrap = $false
$resultBox.Font = New-Object Drawing.Font('Consolas', 10)
$resultBox.Dock = 'Bottom'
$resultBox.Height = 230
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
Add-Label $execTab 'Links (root=target, root==target read-only, root+=target merged):' 20 175
$execLinks = New-Object Windows.Forms.TextBox
$execLinks.Multiline = $true
$execLinks.ScrollBars = 'Vertical'
$execLinks.Location = New-Object Drawing.Point(20, 200)
$execLinks.Size = New-Object Drawing.Size(820, 70)
$execTab.Controls.Add($execLinks)
$execWindows = New-Object Windows.Forms.CheckBox
$execWindows.Text = 'Map C:\Windows read-write'
$execWindows.Location = New-Object Drawing.Point(20, 280)
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
$execPassthrough = New-Object Windows.Forms.CheckBox
$execPassthrough.Text = 'Passthrough executable directory'
$execPassthrough.Location = New-Object Drawing.Point(20, 335)
$execPassthrough.AutoSize = $true
$execTab.Controls.Add($execPassthrough)
$execRoot.Add_CheckedChanged({
    if ($execRoot.Checked) {
        $execPassthrough.Checked = $true
    }
})
$execButton = Add-Button $execTab 'Create silo and run command' 20 365
$execButton.Add_Click({
    Run-Action {
        if (-not $execName.Text -or -not $execCommand.Text) {
            throw 'Enter a silo name and command.'
        }
        $arguments = @('exec', '--detach')
        if ($execRoot.Checked) {
            if (-not $execRootDir.Text) { throw 'Enter a root backing directory.' }
            $arguments += @('--root', $execRootDir.Text)
        }
        if ($execPassthrough.Checked) {
            $arguments += @('--passthrough', 'executable')
        } elseif ($execRoot.Checked) {
            $arguments += @('--no-passthrough', 'executable')
        }
        $hasWindowsRoot = $false
        foreach ($line in ($execLinks.Lines | Where-Object { $_.Trim() })) {
            $link = $line.Trim()
            $arguments += @('--link', $link)
            if ($link -match '(?i)^C:\\(?:Windows(?:\\|=)|=)') {
                $hasWindowsRoot = $true
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
        Start-Bindmount $arguments
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
