[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet('nemotron', 'whisper')]
    [string]$Mode = 'whisper',

    [switch]$Performance,
    [switch]$NoPerformance,

    [string]$EnvFile = '.env'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if ($Performance -and $NoPerformance) {
    throw 'Use either -Performance or -NoPerformance, not both.'
}

$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $repoRoot
$envPath = if ([IO.Path]::IsPathRooted($EnvFile)) { $EnvFile } else { Join-Path $repoRoot $EnvFile }
$enginePort = 8765
$turnPort = 8766
$browserPort = 8080
$children = [Collections.Generic.List[object]]::new()
$script:cancelRequested = $false
$script:interruptRequested = $false
$script:failed = $false
$script:cleanupStarted = $false
$script:nativeJob = $null

if (-not ('YukkuriDev.NativeJob' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Diagnostics;
using System.Runtime.InteropServices;

namespace YukkuriDev {
    public static class CancelState {
        public static volatile bool Requested;
        private static bool installed;

        public static void Install() {
            if (installed) return;
            Console.CancelKeyPress += OnCancel;
            installed = true;
        }

        public static void Uninstall() {
            if (!installed) return;
            Console.CancelKeyPress -= OnCancel;
            installed = false;
        }

        private static void OnCancel(object sender, ConsoleCancelEventArgs e) {
            e.Cancel = true;
            Requested = true;
        }
    }

    public sealed class NativeJob : IDisposable {
        private IntPtr handle;

        [StructLayout(LayoutKind.Sequential)]
        private struct IO_COUNTERS {
            public ulong ReadOperationCount, WriteOperationCount, OtherOperationCount;
            public ulong ReadTransferCount, WriteTransferCount, OtherTransferCount;
        }

        [StructLayout(LayoutKind.Sequential)]
        private struct JOBOBJECT_BASIC_LIMIT_INFORMATION {
            public long PerProcessUserTimeLimit, PerJobUserTimeLimit;
            public uint LimitFlags;
            public UIntPtr MinimumWorkingSetSize, MaximumWorkingSetSize;
            public uint ActiveProcessLimit;
            public UIntPtr Affinity;
            public uint PriorityClass, SchedulingClass;
        }

        [StructLayout(LayoutKind.Sequential)]
        private struct JOBOBJECT_EXTENDED_LIMIT_INFORMATION {
            public JOBOBJECT_BASIC_LIMIT_INFORMATION BasicLimitInformation;
            public IO_COUNTERS IoInfo;
            public UIntPtr ProcessMemoryLimit, JobMemoryLimit, PeakProcessMemoryUsed, PeakJobMemoryUsed;
        }

        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern IntPtr CreateJobObject(IntPtr attributes, string name);
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool SetInformationJobObject(IntPtr job, int infoClass, IntPtr info, uint length);
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool AssignProcessToJobObject(IntPtr job, IntPtr process);
        [DllImport("kernel32.dll")]
        private static extern bool CloseHandle(IntPtr handle);

        public NativeJob() {
            handle = CreateJobObject(IntPtr.Zero, null);
            if (handle == IntPtr.Zero) throw new Win32Exception();
            var info = new JOBOBJECT_EXTENDED_LIMIT_INFORMATION();
            info.BasicLimitInformation.LimitFlags = 0x00002000; // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
            int size = Marshal.SizeOf(info);
            IntPtr pointer = Marshal.AllocHGlobal(size);
            try {
                Marshal.StructureToPtr(info, pointer, false);
                if (!SetInformationJobObject(handle, 9, pointer, (uint)size)) throw new Win32Exception();
            } finally {
                Marshal.FreeHGlobal(pointer);
            }
        }

        public void Add(Process process) {
            if (handle == IntPtr.Zero) throw new ObjectDisposedException("NativeJob");
            if (!AssignProcessToJobObject(handle, process.Handle))
                throw new Win32Exception(Marshal.GetLastWin32Error());
        }

        public void Dispose() {
            IntPtr old = handle;
            handle = IntPtr.Zero;
            if (old != IntPtr.Zero) CloseHandle(old);
            GC.SuppressFinalize(this);
        }

        ~NativeJob() { Dispose(); }
    }
}
'@
}

function Import-DevEnvironment([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Development environment file is missing: $Path`nCopy .env.example to .env and configure local provider paths."
    }
    foreach ($raw in Get-Content -LiteralPath $Path) {
        $line = $raw.Trim()
        if (-not $line -or $line.StartsWith('#')) { continue }
        if ($line.StartsWith('export ')) { $line = $line.Substring(7).TrimStart() }
        $separator = $line.IndexOf('=')
        if ($separator -lt 1) { throw "Invalid .env assignment in $Path" }
        $name = $line.Substring(0, $separator).Trim()
        if ($name -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') { throw "Invalid .env variable name in $Path" }
        if ($null -ne [Environment]::GetEnvironmentVariable($name, 'Process')) { continue }
        $value = $line.Substring($separator + 1).Trim()
        if ($value.Length -ge 2 -and (($value[0] -eq '"' -and $value[-1] -eq '"') -or ($value[0] -eq "'" -and $value[-1] -eq "'"))) {
            $value = $value.Substring(1, $value.Length - 2)
        }
        [Environment]::SetEnvironmentVariable($name, $value, 'Process')
    }
}

function Require-Command([string]$Name, [string]$InstallHint) {
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Required command '$Name' was not found. $InstallHint"
    }
}

function Require-File([string]$Label, [string]$Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) { throw "$Label is not configured in $envPath" }
    $resolved = if ([IO.Path]::IsPathRooted($Path)) { $Path } else { Join-Path $repoRoot $Path }
    if (-not (Test-Path -LiteralPath $resolved -PathType Leaf)) { throw "$Label does not exist: $resolved" }
    return (Resolve-Path -LiteralPath $resolved).Path
}

function Test-ExecutableStarts([string]$Path) {
    $savedErrorPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & $Path --help *> $null
    $code = $LASTEXITCODE
    $ErrorActionPreference = $savedErrorPreference
    return $code -eq 0
}

function Test-PortAvailable([int]$Port) {
    $listener = $null
    try {
        $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $Port)
        $listener.Start()
        return $true
    } catch {
        return $false
    } finally {
        if ($null -ne $listener) { $listener.Stop() }
    }
}

function ConvertTo-NativeArgument([string]$Value) {
    if ($Value -notmatch '[\s"]') { return $Value }
    return '"' + [regex]::Replace($Value, '(\\*)"', '$1$1\"').TrimEnd('\') +
        ('\' * ($Value.Length - $Value.TrimEnd('\').Length)) + '"'
}

function Start-DevChild([string]$Name, [string]$FilePath, [string[]]$Arguments) {
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $FilePath
    $info.WorkingDirectory = $repoRoot
    $info.UseShellExecute = $false
    # Sharing the launcher console is required so one Ctrl+C reaches the Engine,
    # allowing it to cancel and reap its own persistent STT worker first.
    $info.CreateNoWindow = $false
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    if ($info.PSObject.Properties.Name -contains 'ArgumentList') {
        foreach ($argument in $Arguments) { [void]$info.ArgumentList.Add($argument) }
    } else {
        $info.Arguments = (($Arguments | ForEach-Object { ConvertTo-NativeArgument $_ }) -join ' ')
    }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $info
    if (-not $process.Start()) { throw "Failed to start $Name" }
    $script:nativeJob.Add($process)
    $child = [pscustomobject]@{
        Name = $Name
        Process = $process
        Stdout = $process.StandardOutput.ReadLineAsync()
        Stderr = $process.StandardError.ReadLineAsync()
    }
    $children.Add($child)
    return $child
}

function Pump-ChildOutput {
    foreach ($child in $children) {
        foreach ($stream in @('Stdout', 'Stderr')) {
            $task = $child.$stream
            if ($null -ne $task -and $task.IsCompleted) {
                $line = $task.GetAwaiter().GetResult()
                if ($null -ne $line) {
                    $color = if ($stream -eq 'Stderr') { 'Yellow' } else { 'Gray' }
                    Write-Host ("[{0}] {1}" -f $child.Name, $line) -ForegroundColor $color
                    $child.$stream = if ($stream -eq 'Stdout') { $child.Process.StandardOutput.ReadLineAsync() } else { $child.Process.StandardError.ReadLineAsync() }
                } else {
                    $child.$stream = $null
                }
            }
        }
    }
}

function Stop-DevChildren {
    if ($script:cleanupStarted) { return }
    $script:cleanupStarted = $true
    $reverseChildren = @($children)
    [array]::Reverse($reverseChildren)
    if ($script:interruptRequested) {
        # Ctrl+C is delivered to the shared Windows console first. Give the
        # Engine time to cancel its context and reap its owned STT worker.
        $deadline = [DateTime]::UtcNow.AddSeconds(5)
        $engineChild = $children | Where-Object { $_.Name -eq 'Engine' } | Select-Object -First 1
        while ([DateTime]::UtcNow -lt $deadline -and $null -ne $engineChild -and -not $engineChild.Process.HasExited) {
            Pump-ChildOutput
            Start-Sleep -Milliseconds 50
        }
    }
    foreach ($child in $reverseChildren) {
        if (-not $child.Process.HasExited) {
            Write-Host ("Stopping {0}..." -f $child.Name)
            $killer = [Diagnostics.Process]::Start([Diagnostics.ProcessStartInfo]@{
                FileName = 'taskkill.exe'
                Arguments = "/PID $($child.Process.Id) /T /F"
                UseShellExecute = $false
                CreateNoWindow = $true
            })
            if ($null -ne $killer) { [void]$killer.WaitForExit(5000); $killer.Dispose() }
        }
    }
    # Closing the owned Job Object is the final boundary. It also catches a
    # worker whose Engine parent exited before taskkill could walk that tree.
    if ($null -ne $script:nativeJob) {
        $script:nativeJob.Dispose()
        $script:nativeJob = $null
    }
    foreach ($child in $children) {
        if (-not $child.Process.HasExited) { [void]$child.Process.WaitForExit(5000) }
        $child.Process.Dispose()
    }

    $ports = @($enginePort, $turnPort, $browserPort) | Select-Object -Unique
    $deadline = [DateTime]::UtcNow.AddSeconds(5)
    do {
        $listeners = @(Get-NetTCPConnection -LocalPort $ports -State Listen -ErrorAction SilentlyContinue)
        if ($listeners.Count -eq 0) { break }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $deadline)
    foreach ($listener in $listeners) {
        $owner = Get-Process -Id $listener.OwningProcess -ErrorAction SilentlyContinue
        $name = if ($null -ne $owner) { $owner.ProcessName } else { '<exited>' }
        Write-Host ("Cleanup warning: port={0} PID={1} ProcessName={2} is still listening." -f $listener.LocalPort, $listener.OwningProcess, $name) -ForegroundColor Red
        $script:failed = $true
    }
}

try {
    $script:nativeJob = [YukkuriDev.NativeJob]::new()
    [YukkuriDev.CancelState]::Requested = $false
    [YukkuriDev.CancelState]::Install()
    Import-DevEnvironment $envPath
    $env:STT_PROVIDER = $Mode

    Require-Command 'go' 'Install the Go version declared by go.mod.'
    Require-Command 'node' 'Install Node.js 22 or newer.'
    Require-Command 'npm' 'Install npm with Node.js.'
    Require-Command 'taskkill.exe' 'This launcher requires Windows taskkill for child-tree cleanup.'

    $nodeMajor = [int]((& node --version).TrimStart('v').Split('.')[0])
    if ($nodeMajor -lt 22) { throw "Node.js 22 or newer is required; found $(& node --version)." }
    if (-not (Test-Path -LiteralPath 'sdk/typescript/node_modules/typescript' -PathType Container)) {
        throw 'Browser SDK dependencies are missing. Run: npm --prefix sdk/typescript ci'
    }

    $turnPython = Require-File 'Smart Turn Python runtime' 'runtime/turn-detection/venv/Scripts/python.exe'
    $turnModel = Require-File 'Smart Turn model' 'runtime/turn-detection/smart-turn-v3.2-cpu.onnx'
    $turnServer = Require-File 'Smart Turn server' 'tools/turn-detector/server.py'
    $savedErrorPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & $turnPython -c 'import numpy, onnxruntime; from transformers import WhisperFeatureExtractor' 2>$null
    $dependencyExitCode = $LASTEXITCODE
    $ErrorActionPreference = $savedErrorPreference
    if ($dependencyExitCode -ne 0) { throw 'Smart Turn Python dependencies are incomplete. Run: ./tools/turn-detector/setup.ps1' }

    $turnURL = if ($env:TURN_DETECTOR_URL) { [Uri]$env:TURN_DETECTOR_URL } else { [Uri]'http://127.0.0.1:8766/predict' }
    if ($turnURL.Scheme -ne 'http' -or $turnURL.Host -notin @('127.0.0.1', 'localhost')) {
        throw 'TURN_DETECTOR_URL must use loopback HTTP for the development launcher.'
    }
    $turnPort = $turnURL.Port
    foreach ($port in @($enginePort, $turnPort, $browserPort)) {
        if (-not (Test-PortAvailable $port)) { throw "Required loopback port is already in use: $port" }
    }

    $performanceEnabled = -not $NoPerformance
    if ($Performance) { $performanceEnabled = $true }
    if ($Mode -eq 'nemotron') {
        $env:NEMOTRON_THREADS = '2'
        $env:NEMOTRON_LANGUAGE = 'auto'
        $env:NEMOTRON_PERFORMANCE = $performanceEnabled.ToString().ToLowerInvariant()
        $env:NEMOTRON_WORKER_EXECUTABLE = Require-File 'NEMOTRON_WORKER_EXECUTABLE' $env:NEMOTRON_WORKER_EXECUTABLE
        if (-not (Test-ExecutableStarts $env:NEMOTRON_WORKER_EXECUTABLE)) {
            throw "Nemotron worker cannot start or has a missing runtime dependency: $env:NEMOTRON_WORKER_EXECUTABLE"
        }
        $env:NEMOTRON_ENCODER = Require-File 'NEMOTRON_ENCODER' $env:NEMOTRON_ENCODER
        $env:NEMOTRON_DECODER = Require-File 'NEMOTRON_DECODER' $env:NEMOTRON_DECODER
        $env:NEMOTRON_JOINER = Require-File 'NEMOTRON_JOINER' $env:NEMOTRON_JOINER
        $env:NEMOTRON_TOKENS = Require-File 'NEMOTRON_TOKENS' $env:NEMOTRON_TOKENS
    } else {
        $runtime = if ($env:STT_RUNTIME) { $env:STT_RUNTIME } else { 'persistent' }
        $binary = if ($runtime -eq 'process') { 'whisper-cli.exe' } else { 'whisper-server.exe' }
        $model = if ($env:STT_MODEL_PATH) { $env:STT_MODEL_PATH } else {
            $modelName = if ($env:STT_MODEL) { $env:STT_MODEL } else { 'small' }
            "runtime/whisper/models/ggml-$modelName.bin"
        }
        [void](Require-File 'Whisper model' $model)
        $device = if ($env:STT_DEVICE) { $env:STT_DEVICE } else { 'auto' }
        $cpu = if ($env:STT_CPU_EXECUTABLE) { $env:STT_CPU_EXECUTABLE } else { "runtime/whisper/cpu/$binary" }
        $cuda = if ($env:STT_CUDA_EXECUTABLE) { $env:STT_CUDA_EXECUTABLE } else { "runtime/whisper/cuda/$binary" }
        if ($device -eq 'cpu') {
            $cpu = Require-File 'Whisper CPU executable' $cpu
            if (-not (Test-ExecutableStarts $cpu)) { throw "Whisper CPU executable cannot start or has a missing runtime dependency: $cpu" }
        } elseif ($device -eq 'cuda') {
            $cuda = Require-File 'Whisper CUDA executable' $cuda
            if (-not (Test-ExecutableStarts $cuda)) { throw "Whisper CUDA executable cannot start or has a missing runtime dependency: $cuda" }
        } else {
            $workingWhisper = @(@($cuda, $cpu) | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Where-Object { Test-ExecutableStarts $_ })
            if ($workingWhisper.Count -eq 0) {
                throw "No runnable Whisper executable is available for STT_DEVICE=auto. Checked: $cpu and $cuda"
            }
        }
    }

    Write-Host 'Building Browser Voice SDK...'
    & npm --prefix sdk/typescript run build
    if ($LASTEXITCODE -ne 0) { throw 'Browser SDK build failed.' }
    $devRoot = Join-Path $repoRoot '.dev'
    New-Item -ItemType Directory -Force -Path $devRoot | Out-Null
    $engineExecutable = Join-Path $devRoot 'engine.exe'
    Write-Host 'Building Engine...'
    & go build -o $engineExecutable ./cmd/engine
    if ($LASTEXITCODE -ne 0) { throw 'Engine build failed.' }

    Write-Host ''
    Write-Host 'Yukkuri Realtime Engine Dev Launcher' -ForegroundColor Cyan
    Write-Host ("STT          : {0}" -f (Get-Culture).TextInfo.ToTitleCase($Mode))
    if ($Mode -eq 'nemotron') {
        Write-Host 'Runtime      : sherpa-onnx persistent worker'
        Write-Host 'Model        : Nemotron 3.5 Streaming ASR 0.6B 560ms INT8'
        Write-Host 'Threads      : 2'
        Write-Host 'Language     : auto'
        Write-Host ("Performance  : {0}" -f $(if ($performanceEnabled) { 'enabled' } else { 'disabled' }))
    } else {
        Write-Host ("Runtime      : whisper.cpp {0}" -f $(if ($env:STT_RUNTIME) { $env:STT_RUNTIME } else { 'persistent' }))
    }
    Write-Host ("Engine       : http://127.0.0.1:{0}" -f $enginePort)
    Write-Host ("Browser      : http://127.0.0.1:{0}/examples/typescript/browser-voice/" -f $browserPort)
    Write-Host ("Smart Turn   : http://127.0.0.1:{0}" -f $turnPort)
    Write-Host ''

    [void](Start-DevChild 'Turn' $turnPython @($turnServer, '--model', $turnModel, '--port', "$turnPort"))
    [void](Start-DevChild 'Engine' $engineExecutable @())
    [void](Start-DevChild 'Browser' $turnPython @('-u', '-m', 'http.server', "$browserPort", '--bind', '127.0.0.1'))

    while (-not $script:cancelRequested) {
        if ([YukkuriDev.CancelState]::Requested) {
            $script:interruptRequested = $true
            $script:cancelRequested = $true
            break
        }
        Pump-ChildOutput
        foreach ($child in $children) {
            if ($child.Process.HasExited) {
                Write-Host ("[{0}] exited unexpectedly with code {1}." -f $child.Name, $child.Process.ExitCode) -ForegroundColor Red
                $script:failed = $true
                $script:cancelRequested = $true
                break
            }
        }
        Start-Sleep -Milliseconds 40
    }
} catch {
    Write-Host ("Dev launcher error: {0}" -f $_.Exception.Message) -ForegroundColor Red
    $script:failed = $true
} finally {
    Stop-DevChildren
    [YukkuriDev.CancelState]::Uninstall()
}

if ($script:failed) { exit 1 }
