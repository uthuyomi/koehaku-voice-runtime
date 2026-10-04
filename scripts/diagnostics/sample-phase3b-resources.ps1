param(
    [Parameter(Mandatory = $true)][string]$Output,
    [int]$IntervalSeconds = 5
)

$ErrorActionPreference = 'Stop'
$rows = [System.Collections.Generic.List[object]]::new()
while (Get-NetTCPConnection -State Listen -LocalPort 8765 -ErrorAction SilentlyContinue) {
    $now = [DateTimeOffset]::UtcNow.ToString('o')
    $counters = Get-Counter -Counter '\Processor Information(_Total)\% Processor Performance', '\Processor Information(_Total)\% Processor Utility', '\Processor Information(_Total)\Processor Frequency' -MaxSamples 1
    $values = @{}
    foreach ($sample in $counters.CounterSamples) {
        $values[$sample.Path.Split('\')[-1]] = $sample.CookedValue
    }
    foreach ($process in Get-Process -ErrorAction SilentlyContinue | Where-Object {
        $_.ProcessName -in @('engine', 'sherpa-onnx-nemotron-worker', 'python', 'msedge', 'powershell')
    }) {
        $rows.Add([pscustomobject]@{
            Timestamp = $now
            PID = $process.Id
            ProcessName = $process.ProcessName
            CPUSeconds = $process.CPU
            WorkingSetBytes = $process.WorkingSet64
            PrivateBytes = $process.PrivateMemorySize64
            Threads = $process.Threads.Count
            Handles = $process.HandleCount
            ProcessorPerformance = $values['% processor performance']
            ProcessorUtility = $values['% processor utility']
            ProcessorFrequencyMHz = $values['processor frequency']
        })
    }
    Start-Sleep -Seconds $IntervalSeconds
}
$rows | Export-Csv -LiteralPath $Output -NoTypeInformation -Encoding UTF8
