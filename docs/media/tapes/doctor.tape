# `docmatch status` e `docmatch doctor` numa sessão de terminal, na instalação que a fita
# instalador.tape acabou de subir (mesmas variáveis, definidas por tools/media/media.sh).
Output ${MEDIA_CASTS}/doctor.cast
Size 96x30
Title docmatch
Timeout 5m
Env HOME=${MEDIA_HOME}
Env XDG_DATA_HOME=${MEDIA_HOME}/.local/share
Env DOCKER_CONFIG=${MEDIA_DOCKER_CONFIG}
Env DOCMATCH_THEME=dark
Env DOCMATCH_PROJECT=${MEDIA_PROJECT}
Env PATH=${MEDIA_BIN}:${PATH}
Env HISTFILE=/dev/null
Env PS1='\[\e[1;34m\]~\[\e[0m\] $ '
Dir ${MEDIA_HOME}
Run bash --norc --noprofile -i

WaitFor '~ \$' 10s
Marker start
Sleep 1s
Type "docmatch status"
Sleep 400ms
Key Enter
WaitFor '(?s)\$ docmatch status\n.*\n~ \$' 1m
Sleep 5s
Key ctrl+l
WaitFor '^~ \$\s*$' 10s
Sleep 600ms
Type "docmatch doctor"
Sleep 400ms
Key Enter
WaitFor '(?s)\$ docmatch doctor\n.*\n~ \$' 2m
Sleep 6s
Marker end
Type "exit"
Key Enter
WaitExit 10s
