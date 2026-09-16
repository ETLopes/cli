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
}
