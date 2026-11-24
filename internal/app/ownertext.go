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
	"if.blocked":        {"The item stays blocked; agents work on other items.", "O item fica parado; os agentes seguem com os outros."},
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
