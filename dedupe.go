package main

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"google.golang.org/api/people/v1"
)

// clientDataKey marca, em cada contato criado no destino, de qual contato da
// origem ele veio. Assim rodar o programa de novo nunca reimporta o mesmo
// contato, mesmo que alguém tenha editado o email/telefone dele no destino.
const clientDataKey = "syncContactsSourceId"

// Telefones são comparados pelos últimos 8 dígitos. Isso ignora DDI, DDD,
// zeros de operadora e o "9" extra dos celulares brasileiros, de forma que
// "+55 (11) 98765-4321", "011 98765 4321" e "8765-4321" batem entre si.
const phoneSuffixLen = 8

// index guarda as chaves de comparação dos contatos que já existem no destino
// (e dos que já foram escolhidos para criação nesta execução).
type index struct {
	emails    map[string]string // chave -> nome do contato existente
	phones    map[string]string
	names     map[string]string
	sourceIDs map[string]string
}

func newIndex() *index {
	return &index{
		emails:    map[string]string{},
		phones:    map[string]string{},
		names:     map[string]string{},
		sourceIDs: map[string]string{},
	}
}

func (ix *index) add(p *people.Person) {
	label := displayName(p)
	for _, e := range p.EmailAddresses {
		if k := emailKey(e.Value); k != "" {
			ix.emails[k] = label
		}
	}
	for _, ph := range p.PhoneNumbers {
		if k := phoneKey(ph); k != "" {
			ix.phones[k] = label
		}
	}
	if k := nameKey(p); k != "" {
		ix.names[k] = label
	}
	for _, cd := range p.ClientData {
		if cd.Key == clientDataKey && cd.Value != "" {
			ix.sourceIDs[cd.Value] = label
		}
	}
}

// matchKind diz por que um contato foi considerado duplicado.
type matchKind int

const (
	noMatch       matchKind = iota
	matchImported           // copiado por este programa numa execução anterior
	matchEmail
	matchPhone
	matchName
)

// match diz se p já existe no índice e, se sim, por quê.
func (ix *index) match(p *people.Person, sourceID string, useName bool) (matchKind, string) {
	if l, ok := ix.sourceIDs[sourceID]; ok && sourceID != "" {
		return matchImported, fmt.Sprintf("já importado antes como %q", l)
	}
	for _, e := range p.EmailAddresses {
		if l, ok := ix.emails[emailKey(e.Value)]; ok {
			return matchEmail, fmt.Sprintf("email %s já está em %q", e.Value, l)
		}
	}
	for _, ph := range p.PhoneNumbers {
		if l, ok := ix.phones[phoneKey(ph)]; ok {
			return matchPhone, fmt.Sprintf("telefone %s já está em %q", ph.Value, l)
		}
	}
	if useName {
		if l, ok := ix.names[nameKey(p)]; ok {
			return matchName, fmt.Sprintf("mesmo nome de %q", l)
		}
	}
	return noMatch, ""
}

func emailKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func phoneKey(ph *people.PhoneNumber) string {
	d := digits(ph.Value)
	if d == "" {
		d = digits(ph.CanonicalForm)
	}
	if len(d) > phoneSuffixLen {
		d = d[len(d)-phoneSuffixLen:]
	}
	return d
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// nameKey normaliza o nome completo: sem acentos, minúsculo e com espaços
// simples, para que "José  da Silva" e "jose da silva" sejam iguais.
func nameKey(p *people.Person) string {
	if len(p.Names) == 0 {
		return ""
	}
	n := p.Names[0]
	full := strings.Join([]string{n.GivenName, n.MiddleName, n.FamilyName}, " ")
	if strings.TrimSpace(full) == "" {
		full = n.DisplayName
	}
	if strings.TrimSpace(full) == "" {
		full = n.UnstructuredName
	}
	return fold(full)
}

func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// displayName devolve algo legível para identificar o contato nos logs.
func displayName(p *people.Person) string {
	if len(p.Names) > 0 {
		n := p.Names[0]
		for _, s := range []string{n.DisplayName, n.UnstructuredName, strings.TrimSpace(n.GivenName + " " + n.FamilyName)} {
			if s != "" {
				return s
			}
		}
	}
	if len(p.EmailAddresses) > 0 {
		return p.EmailAddresses[0].Value
	}
	if len(p.PhoneNumbers) > 0 {
		return p.PhoneNumbers[0].Value
	}
	if len(p.Organizations) > 0 && p.Organizations[0].Name != "" {
		return p.Organizations[0].Name
	}
	return "(sem nome)"
}
