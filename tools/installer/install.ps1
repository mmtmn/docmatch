# docmatch: instalador de uma linha para Windows (PowerShell 5.1 ou mais novo).
#
#   irm <base>/install.ps1 | iex
#
# Baixa o docmatch.exe deste sistema e o SHA256SUMS.txt da mesma base, confere o checksum,
# instala em %LOCALAPPDATA%\docmatch\bin (sem administrador) e roda o instalador.
#
# Variáveis:
#   DOCMATCH_INSTALL_BASE  base de download (o release.sh grava a padrão neste arquivo)
#   DOCMATCH_VERSION       versão fixa (ex.: 0.1.0); vazio = a mais recente
#   DOCMATCH_BIN_DIR       onde instalar (padrão %LOCALAPPDATA%\docmatch\bin)
#   DOCMATCH_NO_RUN=1      só instala, não roda
#
# Rodado com `| iex`, o script vive na sessão de quem chamou: tudo fica dentro de um bloco e
# erros usam throw (nunca exit, que fecharia a janela do PowerShell).
& {
    Set-StrictMode -Version 2.0
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'

    function Say([string]$Message) { Write-Host "docmatch: $Message" }

    $base = $env:DOCMATCH_INSTALL_BASE
    if (-not $base) { $base = '__DOCMATCH_INSTALL_BASE__' }
    if ($base.StartsWith('__DOCMATCH')) {
        throw 'docmatch: este install.ps1 não foi gerado pelo release.sh; defina DOCMATCH_INSTALL_BASE'
    }
    $base = $base.TrimEnd('/')
    if ($env:DOCMATCH_VERSION) { $base = "$base/v$($env:DOCMATCH_VERSION.TrimStart('v'))" }

    # PowerShell 5.1 may still default to TLS 1.0.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    # A 32-bit PowerShell on a 64-bit Windows reports x86 here and the real one in ...W6432.
    $cpu = $env:PROCESSOR_ARCHITEW6432
    if (-not $cpu) { $cpu = $env:PROCESSOR_ARCHITECTURE }
    switch ($cpu) {
        'AMD64' { $arch = 'amd64' }
        'ARM64' { $arch = 'arm64' }
        default { throw "docmatch: arquitetura não suportada: $cpu" }
    }
    $name = "docmatch-windows-$arch.exe"

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('docmatch-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Say "baixando $name de $base"
        $sums = Join-Path $tmp 'SHA256SUMS.txt'
        $exe = Join-Path $tmp $name
        Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS.txt" -OutFile $sums
        Invoke-WebRequest -UseBasicParsing -Uri "$base/$name" -OutFile $exe

        $expected = $null
        foreach ($line in Get-Content -Path $sums) {
            $parts = $line -split '\s+', 2
            if ($parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq $name) { $expected = $parts[0].ToLowerInvariant() }
        }
        if (-not $expected) { throw "docmatch: $name não está no SHA256SUMS.txt" }
        $actual = (Get-FileHash -Algorithm SHA256 -Path $exe).Hash.ToLowerInvariant()
        if ($actual -ne $expected) {
            throw "docmatch: checksum não confere para $name (esperado $expected, obtido $actual); nada foi instalado"
        }
        Say 'checksum SHA-256 conferido'

        $binDir = $env:DOCMATCH_BIN_DIR
        if (-not $binDir) { $binDir = Join-Path $env:LOCALAPPDATA 'docmatch\bin' }
        New-Item -ItemType Directory -Force -Path $binDir | Out-Null
        $target = Join-Path $binDir 'docmatch.exe'
        Move-Item -Force -Path $exe -Destination $target
        Say "instalado em $target"

        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        if (-not $userPath -or ($userPath -split ';') -notcontains $binDir) {
            Say "dica: para usar o comando docmatch em qualquer terminal, adicione $binDir ao PATH:"
            Say "  [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ';$binDir', 'User')"
        }
    }
    finally {
        Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
    }

    if ($env:DOCMATCH_NO_RUN -eq '1') { return }
    & $target
}
