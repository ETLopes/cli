package i18n

// pt is Brazilian Portuguese.
//
// Console labels stay in English on purpose. TRIM, PAN, MUTE, SOLO and EQ are
// printed that way on the panel of every desk sold here, so translating them
// would make the layout less familiar, not more. The prose is what carries the
// meaning, and that is translated in full.
var pt = map[string]string{
	"ctrl.trim": "Nível de entrada, antes de tudo o mais. Aumente se a fonte estiver " +
		"fraca demais para registrar; diminua se o sinal estiver distorcendo. Ajuste " +
		"isto primeiro: tudo abaixo reage ao que recebe daqui.",
	"ctrl.high": "Agudos. Realce para ganhar brilho, respiração e definição de cordas; " +
		"corte para domar aspereza, prato estourado ou sibilância.",
	"ctrl.mid": "A faixa onde vive a maior parte dos instrumentos, então é ela que decide " +
		"o que soa presente e o que soa enterrado. Cortar aqui geralmente abre espaço; " +
		"realçar traz a fonte para a frente e pode deixá-la encaixotada ou anasalada.",
	"ctrl.midfreq": "Em qual frequência o controle MID atua. Varra a faixa realçando para " +
		"achar a nota incômoda, depois corte ali. Poucas centenas de Hz é som encaixotado, " +
		"perto de 1k é anasalado, 3-4k é presença e aspereza.",
	"ctrl.low": "Graves. Realce para ganhar peso e corpo; corte para tirar ronco, batida " +
		"no microfone, ou a sujeira de vários instrumentos disputando a mesma região grave.",
	"ctrl.comp": "O quanto o compressor trabalha. Ele deixa as partes altas mais baixas, " +
		"o que equilibra a performance e permite subir o nível geral. Um pouco estabiliza " +
		"um vocal ou um baixo; muito achata a dinâmica e começa a bombear.",
	"ctrl.pan": "Onde o canal fica entre a caixa esquerda e a direita. Espalhar as fontes " +
		"evita que uma mascare a outra; mantenha baixo, bumbo e vocal principal perto do centro.",
	"ctrl.mute": "Silencia este canal em todos os lugares, inclusive nos mixes de fone.",
	"ctrl.solo": "Escuta este canal sozinho. Enquanto houver algum solo ativo, tudo que " +
		"não estiver em solo fica mudo. Útil para caçar um problema; fácil de esquecer ligado.",
	"ctrl.fader": "O nível do canal no mix principal. Não afeta os mixes de fone: eles são " +
		"tirados antes do fader, então você muda o mix da sala sem mexer no que os músicos " +
		"estão ouvindo.",
	"ctrl.aux": "Quanto deste canal vai para o mix de fone %d. Cada mix é o que um músico " +
		"ouve, independente dos outros e do fader principal.",

	"console.keys": "←/→ canal · ↑/↓ controle · +/- ajustar (shift ×4) · espaço alterna · " +
		"? ajuda · tab muda de página · s salvar · q sair",
	"console.showing":    "mostrando %d-%d de %d entradas",
	"console.no_inputs":  "nenhuma entrada configurada",
	"console.limit":      "%s está no limite",
	"console.saved":      "salvo em %s",
	"console.connecting": "reconectando...",

	"studio.connected":     "conectado a %s",
	"studio.disconnected":  "REAPER não conectado",
	"studio.matches":       "o REAPER está igual à sessão",
	"studio.differences":   "%d diferença(s) em relação à sessão:",
	"studio.sync_hint":     "Rode 'cli studio sync' para o REAPER acompanhar.",
	"studio.saved_session": "salvo na sessão; rode 'cli studio sync' quando o REAPER estiver disponível.",

	"setup.unchanged": "já está configurado; nada a mudar",
	"setup.summary":   "%d criado(s), %d reparado(s), %d sem alteração",
	"setup.restart":   "Reinicie o REAPER para ele carregar a ponte.",
	"setup.next":      "Agora: abra o REAPER e rode 'cli studio init' de novo.",
	"setup.next_hint": "Essa segunda execução monta as trilhas, os barramentos e o roteamento.",

	"monitor.muted":   "monitores mudos",
	"monitor.unmuted": "monitores ativados",

	"cmd.studio.short":  "Controla um home studio baseado no REAPER",
	"cmd.dtx.short":     "Transforma qualquer vídeo em faixas de estudo para o DTX-PRO",
	"cmd.cue.short":     "Ajusta o nível de um instrumento em um mix de fone",
	"cmd.init.short":    "Configura o REAPER do zero: interface web, ponte, topologia",
	"cmd.setup.short":   "Cria ou repara a topologia do estúdio no REAPER",
	"cmd.status.short":  "Mostra a conexão e as diferenças em relação à sessão",
	"cmd.sync.short":    "Faz o REAPER ficar igual à sessão",
	"cmd.monitor.short": "Controla os monitores da sala",

	"toolbox.tagline":       "uma caixa de ferramentas pessoal",
	"toolbox.launcher_keys": "↑/↓ ou j/k para mover · enter para executar · 1-9 para pular · q para sair",
	"tool.dtx.short":        "Transforma um vídeo em faixas de estudo para bateria DTX-PRO",
	"tool.studio.short":     "Comanda um home studio no REAPER: mixes de fone, efeitos, monitoração",

	"set.group.general": "Geral",
	"set.group.studio":  "Estúdio",
	"set.group.dtx":     "Faixas de estudo DTX",
	"set.lang":          "Idioma",
	"set.lang.help":     "O idioma em que tudo é exibido. Vale imediatamente.",
	"set.reaper_host":   "Host do REAPER",
	"set.reaper_host.help": "Onde a interface web do REAPER escuta. Deixe em 127.0.0.1 a não " +
		"ser que o REAPER rode em outra máquina: a interface web não tem senha por padrão.",
	"set.reaper_port":      "Porta do REAPER",
	"set.reaper_port.help": "Precisa ser a mesma porta definida em Preferências, em Control/OSC/web.",
	"set.session":          "Sessão",
	"set.session.help":     "Qual sessão salva abre por padrão.",
	"set.session_dir":      "Pasta das sessões",
	"set.session_dir.help": "Onde os arquivos de sessão ficam guardados.",
	"set.model":            "Modelo de separação",
	"set.model.help": "Qual modelo separa a faixa em stems. Separação melhor custa " +
		"proporcionalmente mais tempo.",
	"set.model.standard":  "4 stems, mais rápido",
	"set.model.finetuned": "4 stems, mais limpo, cerca de 4x mais lento",
	"set.model.sixstem":   "adiciona guitarra e piano, experimental",
	"set.device":          "Processamento",
	"set.device.help": "O que faz o trabalho de separação. Auto escolhe a GPU no Apple Silicon " +
		"e volta para o processador se falhar.",
	"set.device.auto":     "auto — GPU se houver",
	"set.formats":         "Formatos para compartilhar",
	"set.formats.help":    "Formatos extras gerados junto com os WAVs do módulo. Escolha entre: %s",
	"set.output_dir":      "Pasta de saída",
	"set.output_dir.help": "Onde as faixas prontas são gravadas.",
	"set.normalize":       "Normalizar volume",
	"set.normalize.help": "Equaliza o volume de todos os mixes gerados. Altera a dinâmica da " +
		"gravação, então fica desligado a não ser que um mix soe baixo perto dos outros.",
	"set.limit":         "Limitador",
	"set.limit.help":    "Evita clipping ao somar os stems. Raramente necessário.",
	"set.usb_path":      "Pendrive",
	"set.usb_path.help": "Copia os WAVs prontos para cá ao final. Deixe vazio para pular.",
	"set.on":            "ligado",
	"set.off":           "desligado",
	"set.unset":         "não definido",
	"set.err.number":    "precisa ser um número",
	"set.keys":          "↑/↓ opção · ←/→ alterar · enter editar · s salvar · q sair",
	"set.keys.editing":  "digite para editar · enter confirma · esc cancela",
	"set.saved":         "salvo",
	"set.unsaved":       "alterações não salvas",
	"set.title":         "configurações",

	"patch.input":     "entrada",
	"patch.inputs":    "entradas",
	"patch.mono":      "mono",
	"patch.stereo":    "estéreo",
	"patch.conflict":  "conflita com %s",
	"patch.free":      "entradas livres",
	"patch.keys":      "↑/↓ entrada · ←/→ canal · t instrumento · a adicionar · d remover · m mono/estéreo · s salvar · q sair",
	"patch.saved":     "salvo e aplicado no REAPER",
	"patch.unsaved":   "não salvo — aperte s para aplicar",
	"patch.conflicts": "resolva os conflitos antes de salvar",
	"patch.nowis":     "agora é %s — os efeitos mudam junto",
	"patch.added":     "adicionado na entrada %d",
	"patch.removed":   "%s desconectado",
	"patch.full":      "todas as %d entradas estão em uso",
	"patch.last":      "o estúdio precisa de pelo menos uma entrada",

	// O que está ligado em cada entrada, que define a cadeia de efeitos.
	"kind.vocal":  "Voz",
	"kind.guitar": "Guitarra",
	"kind.bass":   "Baixo",
	"kind.keys":   "Teclado",
	"kind.drums":  "Bateria",
	"kind.line":   "Linha",
}
