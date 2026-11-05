# O instalador na primeira vez, no modo Experimentar: abertura, modo, verificações, escolhas,
# instalação (compila as imagens a partir do código) e o gêmeo da Vantara conciliado.
#
# Gravada por tools/media/media.sh (make media), que define as variáveis: uma pasta de usuário só
# da gravação (MEDIA_HOME, para os caminhos na tela não mostrarem a máquina de quem grava), o
# binário do instalador, um projeto do Compose e portas que não são as do `make local`. A espera
# pela compilação passa acelerada (o GIF mostra "acelerado N×" na barra).
Output ${MEDIA_CASTS}/instalador.cast
Size 92x30
Title docmatch
Timeout 60m
Env HOME=${MEDIA_HOME}
Env XDG_DATA_HOME=${MEDIA_HOME}/.local/share
Env DOCKER_CONFIG=${MEDIA_DOCKER_CONFIG}
Env DOCMATCH_THEME=dark
Dir ${MEDIA_HOME}
Run ${MEDIA_BIN}/docmatch --project ${MEDIA_PROJECT} --registry docmatch.local --tag ${MEDIA_TAG} --build --no-browser --ports ${MEDIA_PORTS} --source ${MEDIA_HOME}/docmatch

# Abertura: o brilho passa uma vez.
WaitFor "Enter começar" 30s
Sleep 2.5s
Key Enter

# Modo: Experimentar (só Docker) já vem selecionado.
WaitFor "Desenvolver" 10s
Sleep 2.5s
Key Enter

# Verificações, uma linha por vez.
WaitFor "Enter continuar" 2m
Sleep 2.5s
Key Enter

# Escolhas: modelo desligado, as portas sugeridas e a pasta padrão.
WaitFor "Modelo de linguagem" 10s
Sleep 2.5s
Key Enter
WaitFor "Portas" 10s
Sleep 2s
Key Enter x4 @350ms
WaitFor "Pasta da instalação" 10s
Sleep 2s
Key Enter

# Instalação: a compilação das imagens passa 40 vezes mais rápido; o resto, 3 vezes.
WaitFor "Instalando a docmatch" 30s
WaitFor "Compilar as imagens" 5m
Marker speed=40
WaitFor "✓ Compilar as imagens" 45m
Marker speed=3
WaitFor "docmatch no ar" 15m
Marker speed=1
Sleep 8s
Marker end
Key Enter
WaitExit 1m
