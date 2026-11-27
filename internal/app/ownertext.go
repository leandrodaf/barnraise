package app

import (
	"fmt"
	"strings"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Owner-facing text of the questions Barnraise itself asks. Agents and code
// stay in English; what a person reads comes out in the project's language
// (`language:` in barnraise.yaml). English is the fallback.
var ownerTexts = map[string][2]string{ // key: {en, pt-BR}
	"blocked.context":   {"%s (%s) is blocked at step %s: %s", "%s (%s) parou na etapa %s: %s"},
	"continue.decision": {"How should it continue?", "Como seguir?"},
	"opt.retry":         {"Retry the step", "Tentar a etapa de novo"},
	"opt.abandon":       {"Abandon the item", "Desistir do item"},
	"opt.done":          {"Already done another way: close the item", "Já está resolvido de outro jeito: fechar o item"},
	"if.blocked":        {"The item stays blocked; agents work on other items.", "O item fica parado; os agentes seguem com os outros."},
	"blocked.again": {"%s (%s) is blocked again at step %s (attempt %d), with nothing new to wait for. The autopilot decides at %s, or sooner if something lands on main, instead of retrying at once against the same wall. %s",
		"%s (%s) parou de novo na etapa %s (tentativa %d), sem nada novo a esperar. O piloto automático decide às %s, ou antes se algo entrar na main, em vez de tentar de novo na hora contra a mesma parede. %s"},
	"if.blocked.again":  {"The item waits until then; agents work on other items and no session is spent on it.", "O item espera até lá; os agentes seguem com os outros e nenhuma sessão é gasta nele."},
	"needs.context":     {"%s (%s) needs information at step %s.", "%s (%s) precisa de uma informação na etapa %s."},
	"opt.answer":        {"Answer and continue", "Responder e seguir"},
	"if.waits":          {"The item waits; agents work on other items.", "O item espera; os agentes seguem com os outros."},
	"rework.context":    {"%s (%s) came back %d times; last reason: %s", "%s (%s) voltou %d vezes; último motivo: %s"},
	"opt.stronger":      {"Retry with a stronger model", "Tentar de novo com um modelo mais forte"},
	"opt.accept":        {"Accept with a caveat", "Aceitar com ressalva"},
	"opt.split":         {"Split the item", "Dividir o item"},
	"approval.context":  {"%s (%s) is at step %s.", "%s (%s) está na etapa %s."},
	"approval.decision": {"Approve the %s of %s?", "Aprova %s do %s?"},
	"opt.approve":       {"Approve the %s", "Aprovar %s"},
	"opt.rework":        {"Send back for rework", "Mandar refazer"},
	"what.plan":         {"plan", "o plano"},
	"what.delivery":     {"delivery", "a entrega"},
	"conflict.context":  {"%s contradicts %s: %s", "%s contradiz %s: %s"},
	"conflict.decision": {"Keep the item as it is, update it, or close it?", "Manter o item como está, atualizar ou fechar?"},
	"opt.update":        {"Update the item", "Atualizar o item"},
	"opt.keep":          {"Keep as is", "Manter como está"},
	"if.conflict":       {"The item stays out of the queue.", "O item fica fora da fila."},
	"goal.context":      {"Goal %s (%s) is done: %d of %d items.", "O objetivo %s (%s) terminou: %d de %d itens."},
	"goal.decision":     {"What is the next goal?", "Qual é o próximo objetivo?"},
	"opt.new_goal":      {"Set a new goal", "Definir um novo objetivo"},
	"opt.bugs_only":     {"Only bugs and approved improvements for now", "Por enquanto, só bugs e melhorias aprovadas"},
	"if.goal":           {"Agents work only on bugs and approved improvements.", "Os agentes trabalham só em bugs e melhorias aprovadas."},
	"answered.message":  {"Owner answered: %s", "O dono respondeu: %s"},
	"delegate.ask":      {"Decide for me: choose the answer you recommend.", "Decida por mim: escolha a resposta que você recomenda."},
	"delegate.note":     {"Decided by the advisor at the owner's request: %s", "Decidido pelo conselheiro a pedido do dono: %s"},
	"delegate.done":     {"Applied: %s.", "Aplicado: %s."},
	"autopilot.reason":  {"Decided by the advisor on autopilot: %s", "Decidido pelo conselheiro no piloto automático: %s"},
	"autopilot.ask": {"Autopilot: the owner is away and asked you to take this decision. Choose the answer you recommend and say why. " +
		"Only when the decision truly needs the owner (signing in to an account, spending money, a physical device or place, giving an access or a secret, " +
		"a legal, brand or personal choice) end with option: OWNER and, in the note, exactly what the owner must do. " +
		"Anything Barnraise itself can do is not the owner's: recording criteria or dependencies (option: EDIT), unlocking files (option: UNLOCK), " +
		"retrying, or choosing the option that moves the item on. An earlier owner answer that forbade a workaround does not forbid these. " +
		"When the step is blocked because what it asks is already in place another way (the owner did it, or switched it on in the panel), choose the option that closes the item as already done, so the items that wait for it go on. " +
		"When no listed option fits, choose the one closest to moving the item on and say in the note what to do.",
		"Piloto automático: o dono está ausente e pediu que você tome esta decisão. Escolha a resposta que recomenda e diga por quê. " +
			"Só quando a decisão realmente precisa do dono (entrar numa conta, gastar dinheiro, um aparelho ou lugar físico, dar um acesso ou um segredo, " +
			"uma escolha legal, de marca ou pessoal) termine com option: OWNER e, na nota, exatamente o que o dono precisa fazer. " +
			"O que o próprio Barnraise consegue fazer não é do dono: gravar critérios ou dependências (option: EDIT), liberar arquivos (option: UNLOCK), " +
			"tentar de novo ou escolher a opção que faz o item andar. Uma resposta antiga do dono que proibiu contornar não proíbe isso. " +
			"Quando a etapa parou porque o que ela pede já está feito de outro jeito (o dono fez, ou ligou no painel), escolha a opção que fecha o item como já resolvido, para os itens que esperam por ele seguirem. " +
			"Quando nenhuma opção listada serve, escolha a mais próxima de fazer o item andar e diga na nota o que fazer."},
	"autopilot.approve": {"\nThe decision is the approval of %s of this item: the options are approve or send back.", "\nA decisão é a aprovação %s deste item: as opções são approve ou send back."},
	"autopilot.left":    {"Left to you: %s", "Ficou para você: %s"},
	"chat.from":         {"Written from the owner's conversation %s.", "Escrito a partir da conversa %s do dono."},
	"chat.created":      {"Created %s: %s. The analyst refines it and the agents take it from the queue.", "Criei o %s: %s. O analista refina e os agentes pegam da fila."},
	"advisor.none":      {"no Claude account can run the advisor on this machine", "nenhuma conta Claude pode rodar o conselheiro nesta máquina"},
	"advisor.limited":   {"every account is at its limit, in every model", "todas as contas estão no limite, em todos os modelos"},
	"advisor.until":     {"every account is at its limit, in every model, until %s", "todas as contas estão no limite, em todos os modelos, até %s"},
	"autopilot.failed":  {"The autopilot could not ask the advisor on %s; it tries again later. The error: %s", "O piloto automático não conseguiu consultar o conselheiro em %s; tenta de novo mais tarde. O erro: %s"},
	"autopilot.done":    {"Decided on autopilot: %s.", "Decidido no piloto automático: %s."},
}

// ownerPortuguese reports whether the project's people read Portuguese.
func ownerPortuguese(lang string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "pt")
}

// ot is the owner text for key in the project's language.
func ot(p *Project, key string, args ...any) string {
	pair, ok := ownerTexts[key]
	if !ok {
		return key
	}
	f := pair[0]
	if p != nil && ownerPortuguese(p.Cfg().Language) {
		f = pair[1]
	}
	if len(args) == 0 {
		return f
	}
	return fmt.Sprintf(f, args...)
}

// opt builds an option Barnraise offers: the label in the owner's language,
// the action fixed.
func ownerOpt(p *Project, action string, recommended bool, args ...any) domain.Option {
	return domain.Option{Label: ot(p, "opt."+action, args...), Recommended: recommended, Action: action}
}

// ownerLanguageNames names the languages the owner may read, for an agent.
var ownerLanguageNames = map[string]string{"pt": "Brazilian Portuguese", "es": "Spanish", "fr": "French", "de": "German", "it": "Italian"}

// ownerLanguageName names the project's language for an agent; "" for
// English or a language it does not know.
func ownerLanguageName(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if l == "" || strings.HasPrefix(l, "en") {
		return ""
	}
	if n, ok := ownerLanguageNames[strings.SplitN(l, "-", 2)[0]]; ok {
		return n
	}
	return lang
}

// ownerLanguageRule tells an agent to write what the owner reads in the
// project's language: the agent's questions reach a person as written.
func ownerLanguageRule(lang string) string {
	name := ownerLanguageName(lang)
	if name == "" {
		return ""
	}
	return "\n- The owner reads " + name + ". Write every question to the owner (`ask_owner`: context, decision, options and if_no_answer) " +
		"and every note meant for the owner in " + name + ", plainly, without code identifiers unless the decision is about them. " +
		"Code, commits, `change` and messages to other agents stay in English.\n"
}
