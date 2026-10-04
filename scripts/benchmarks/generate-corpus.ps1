param(
    [string]$Manifest = "$PSScriptRoot\corpus-manifest.json",
    [string]$OutputDirectory = "$PSScriptRoot\..\..\runtime\benchmark-corpus"
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Speech
$corpus = Get-Content -LiteralPath $Manifest -Raw | ConvertFrom-Json
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$available = [System.Speech.Synthesis.SpeechSynthesizer]::new().GetInstalledVoices().VoiceInfo.Name
foreach ($sample in $corpus.samples) {
    if ($sample.voice -notin $available) {
        throw "Required local speech voice is unavailable: $($sample.voice)"
    }
    $path = Join-Path $OutputDirectory $sample.file
    $synth = [System.Speech.Synthesis.SpeechSynthesizer]::new()
    try {
        $synth.SelectVoice($sample.voice)
        $format = [System.Speech.AudioFormat.SpeechAudioFormatInfo]::new(
            16000,
            [System.Speech.AudioFormat.AudioBitsPerSample]::Sixteen,
            [System.Speech.AudioFormat.AudioChannel]::Mono
        )
        $synth.SetOutputToWaveFile($path, $format)
        $synth.Speak($sample.reference)
    } finally {
        $synth.Dispose()
    }
    Write-Host "Generated $($sample.id): $path"
}
