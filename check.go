package main

// Modo -check: confere na conta de destino o que aconteceu com cada contato
// salvo nos backups (backup_origem_*.json), ou seja, com os contatos que
// foram apagados da origem. Não altera nada em nenhuma das contas.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/api/option"
	"google.golang.org/api/people/v1"
)

type checkStatus int

const (
	checkImported checkStatus = iota // criado no destino por este programa
	checkEmail                       // não foi criado: o email já existia no destino
	checkPhone                       // não foi criado: o telefone já existia no destino
	checkName                        // só existe no destino alguém com o mesmo nome
	checkMissing                     // nada no destino corresponde a ele
)

type checkResult struct {
	src    *people.Person
	status checkStatus
	dst    *people.Person // contato correspondente no destino (nil em checkMissing)
	reason string
	lost   []string // dados do contato da origem que não estão no do destino
}

// dstIndex indexa os contatos do destino guardando o próprio contato, para
// depois comparar os dados com os da origem.
type dstIndex struct {
	bySourceRN map[string]*people.Person // resourceName na origem -> contato criado
	emails     map[string]*people.Person
	phones     map[string]*people.Person
	names      map[string]*people.Person
}

func newDstIndex(contacts []*people.Person) *dstIndex {
	ix := &dstIndex{
		bySourceRN: map[string]*people.Person{},
		emails:     map[string]*people.Person{},
		phones:     map[string]*people.Person{},
		names:      map[string]*people.Person{},
	}
	put := func(m map[string]*people.Person, k string, p *people.Person) {
		if _, ok := m[k]; k != "" && !ok {
			m[k] = p
		}
	}
	for _, p := range contacts {
		for _, cd := range p.ClientData {
			// O valor é "<email da origem>|<resourceName na origem>". O backup
			// não guarda o email da origem, então a busca é pelo resourceName.
			if cd.Key != clientDataKey {
				continue
			}
			if _, rn, ok := strings.Cut(cd.Value, "|"); ok {
				put(ix.bySourceRN, rn, p)
			}
		}
		for _, e := range p.EmailAddresses {
			put(ix.emails, emailKey(e.Value), p)
		}
		for _, ph := range p.PhoneNumbers {
			put(ix.phones, phoneKey(ph), p)
		}
		put(ix.names, nameKey(p), p)
	}
	return ix
}

func (ix *dstIndex) classify(p *people.Person) checkResult {
	r := checkResult{src: p, status: checkMissing}
	if d, ok := ix.bySourceRN[p.ResourceName]; ok && p.ResourceName != "" {
		r.status, r.dst = checkImported, d
		r.reason = fmt.Sprintf("copiado pelo programa como %q", displayName(d))
	}
	if r.dst == nil {
		for _, e := range p.EmailAddresses {
			if d, ok := ix.emails[emailKey(e.Value)]; ok {
				r.status, r.dst = checkEmail, d
				r.reason = fmt.Sprintf("não foi criado: o email %s já estava em %q", e.Value, displayName(d))
				break
			}
		}
	}
	if r.dst == nil {
		for _, ph := range p.PhoneNumbers {
			if d, ok := ix.phones[phoneKey(ph)]; ok && phoneKey(ph) != "" {
				r.status, r.dst = checkPhone, d
				r.reason = fmt.Sprintf("não foi criado: o telefone %s já estava em %q", ph.Value, displayName(d))
				break
			}
		}
	}
	if r.dst == nil {
		if d, ok := ix.names[nameKey(p)]; ok && nameKey(p) != "" {
			r.status, r.dst = checkName, d
			r.reason = fmt.Sprintf("só existe %q, com o mesmo nome; pode ser outra pessoa", displayName(d))
		}
	}
	if r.dst == nil {
		r.reason = "nenhum contato no destino tem a marca do programa, nem o mesmo email, telefone ou nome"
		return r
	}
	r.lost = lostData(p, r.dst)
	return r
}

// lostData lista o que o contato da origem tem e o do destino não tem.
func lostData(src, dst *people.Person) []string {
	var out []string
	emails := map[string]bool{}
	for _, e := range dst.EmailAddresses {
		emails[emailKey(e.Value)] = true
	}
	for _, e := range src.EmailAddresses {
		if k := emailKey(e.Value); k != "" && !emails[k] {
			out = append(out, "email "+e.Value)
		}
	}
	phones := map[string]bool{}
	for _, ph := range dst.PhoneNumbers {
		phones[phoneKey(ph)] = true
	}
	for _, ph := range src.PhoneNumbers {
		if k := phoneKey(ph); k != "" && !phones[k] {
			out = append(out, "telefone "+ph.Value)
		}
	}
	fields := []struct {
		name     string
		src, dst int
	}{
		{"endereço", len(src.Addresses), len(dst.Addresses)},
		{"empresa", len(src.Organizations), len(dst.Organizations)},
		{"aniversário", len(src.Birthdays), len(dst.Birthdays)},
		{"notas", len(src.Biographies), len(dst.Biographies)},
		{"apelido", len(src.Nicknames), len(dst.Nicknames)},
		{"site", len(src.Urls), len(dst.Urls)},
		{"datas", len(src.Events), len(dst.Events)},
		{"parentes", len(src.Relations), len(dst.Relations)},
		{"campos personalizados", len(src.UserDefined), len(dst.UserDefined)},
	}
	for _, f := range fields {
		if f.src > 0 && f.dst == 0 {
			out = append(out, f.name)
		}
	}
	return out
}

// loadBackups lê os arquivos de backup, ignorando contatos repetidos entre eles.
func loadBackups(paths []string) ([]*people.Person, error) {
	seen := map[string]bool{}
	var out []*people.Person
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var persons []*people.Person
		if err := json.Unmarshal(b, &persons); err != nil {
			return nil, fmt.Errorf("lendo backup %s: %w", path, err)
		}
		for _, p := range persons {
			if p.ResourceName != "" {
				if seen[p.ResourceName] {
					continue
				}
				seen[p.ResourceName] = true
			}
			out = append(out, p)
		}
	}
	return out, nil
}

func runCheck(ctx context.Context, o options, files []string) error {
	if len(files) == 0 {
		files, _ = filepath.Glob("backup_origem_*.json")
		if len(files) == 0 {
			return fmt.Errorf("nenhum backup_origem_*.json na pasta atual; passe os arquivos: -check ARQ...")
		}
	}
	backup, err := loadBackups(files)
	if err != nil {
		return err
	}
	fmt.Printf("%d contatos em %d backup(s): %s\n", len(backup), len(files), strings.Join(files, ", "))

	if err := os.MkdirAll(o.tokenDir, 0o700); err != nil {
		return err
	}
	dstTok := filepath.Join(o.tokenDir, "token_destino.json")
	if o.relogin {
		os.Remove(dstTok)
	}
	dst, err := authenticate(ctx, o.credentials, dstTok, "DESTINO", people.ContactsScope)
	if err != nil {
		return err
	}
	svc, err := people.NewService(ctx, option.WithHTTPClient(dst.Client))
	if err != nil {
		return err
	}
	contacts, err := listContacts(ctx, svc)
	if err != nil {
		return fmt.Errorf("lendo contatos do destino: %w", err)
	}
	fmt.Printf("Destino: %s (%d contatos)\n", dst.Email, len(contacts))

	ix := newDstIndex(contacts)
	byStatus := map[checkStatus][]checkResult{}
	var withLoss []checkResult
	for _, p := range backup {
		r := ix.classify(p)
		byStatus[r.status] = append(byStatus[r.status], r)
		if r.status != checkName && len(r.lost) > 0 {
			withLoss = append(withLoss, r)
		}
	}

	fmt.Printf("\nResumo:\n")
	fmt.Printf("  %4d copiados pelo programa\n", len(byStatus[checkImported]))
	fmt.Printf("  %4d não foram criados porque o email já existia no destino\n", len(byStatus[checkEmail]))
	fmt.Printf("  %4d não foram criados porque o telefone já existia no destino\n", len(byStatus[checkPhone]))
	fmt.Printf("  %4d só têm no destino alguém com o mesmo nome\n", len(byStatus[checkName]))
	fmt.Printf("  %4d NÃO estão no destino\n", len(byStatus[checkMissing]))
	fmt.Printf("  %4d estão no destino, mas faltando algum dado\n", len(withLoss))

	printSection("NÃO estão no destino", byStatus[checkMissing], true)
	printSection("Só bateram pelo nome (confira se é a mesma pessoa)", byStatus[checkName], true)
	printSection("Estão no destino, mas faltando dados", withLoss, false)
	if o.verbose {
		printSection("Copiados pelo programa", byStatus[checkImported], false)
		printSection("Já existiam no destino (email)", byStatus[checkEmail], false)
		printSection("Já existiam no destino (telefone)", byStatus[checkPhone], false)
	}

	if n := len(byStatus[checkMissing]); n > 0 {
		fmt.Printf("\nOs %d contatos que não estão no destino provavelmente foram apagados ou\n"+
			"editados lá depois da cópia (veja a Lixeira do Google Contacts). Os dados\n"+
			"completos deles estão nos arquivos de backup.\n", n)
	}
	if len(byStatus[checkMissing])+len(byStatus[checkName])+len(withLoss) == 0 {
		fmt.Println("\nTudo certo: todos os contatos do backup estão no destino, sem perda de dados.")
	}
	return nil
}

func printSection(title string, rs []checkResult, showContact bool) {
	if len(rs) == 0 {
		return
	}
	rs = append([]checkResult(nil), rs...)
	sort.SliceStable(rs, func(i, j int) bool { return fold(displayName(rs[i].src)) < fold(displayName(rs[j].src)) })
	fmt.Printf("\n%s (%d):\n", title, len(rs))
	for _, r := range rs {
		fmt.Printf("  - %s — %s\n", displayName(r.src), r.reason)
		if showContact {
			if s := contactSummary(r.src); s != "" {
				fmt.Printf("      na origem: %s\n", s)
			}
		}
		if len(r.lost) > 0 {
			fmt.Printf("      falta no destino: %s\n", strings.Join(r.lost, ", "))
		}
	}
}

// contactSummary mostra emails e telefones do contato, para ajudar a achá-lo.
func contactSummary(p *people.Person) string {
	var parts []string
	for _, e := range p.EmailAddresses {
		parts = append(parts, e.Value)
	}
	for _, ph := range p.PhoneNumbers {
		parts = append(parts, ph.Value)
	}
	return strings.Join(parts, ", ")
}
