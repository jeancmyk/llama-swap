# llama-swap launcher for Windows (PowerShell)
# usage: .\scripts\start.ps1 [-Config path\to\config.yaml] [-Listen 127.0.0.1:8080]
param(
    [string]$Config = (Join-Path $PSScriptRoot "..\config.starter.yaml"),
    [string]$Listen = "127.0.0.1:8080"
)

$ErrorActionPreference = "Stop"

foreach ($var in "LLAMACPP_SERVER", "LLM_MODELS_DIR") {
    $value = [System.Environment]::GetEnvironmentVariable($var)
    if ([string]::IsNullOrWhiteSpace($value)) {
        Write-Error @"
Required environment variable $var is not set. Example:
  `$env:LLAMACPP_SERVER = 'E:\llama.cpp\build\bin\Release\llama-server.exe'
  `$env:LLM_MODELS_DIR  = 'E:\LLM Models'
(To persist: setx LLAMACPP_SERVER "..." ; setx LLM_MODELS_DIR "...")
"@
    }
}

$candidates = @(
    (Join-Path $PSScriptRoot "..\build\llama-swap-windows-amd64.exe"),
    (Join-Path $PSScriptRoot "..\build\llama-swap.exe")
)
$exe = $candidates | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $exe) {
    $found = Get-Command llama-swap -ErrorAction SilentlyContinue
    if ($found) { $exe = $found.Source }
}
if (-not $exe) {
    Write-Error "llama-swap binary not found. Build it first (see SETUP.md) or install it in PATH."
}

Write-Host "starting: $exe --config $Config --listen $Listen"
& $exe --config $Config --listen $Listen @args
