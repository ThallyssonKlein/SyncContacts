// synccontacts copia os contatos de uma conta Google para outra sem criar
// duplicatas.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"google.golang.org/api/option"
	"google.golang.org/api/people/v1"
)

type options struct {
	credentials string
	tokenDir    string
	dryRun      bool
	matchName   bool
	labels      bool
	yes         bool
	relogin     bool
	verbose     bool
	batchSize   int

	deleteSource     bool
	deleteDuplicates bool

	check bool
}

func main() {
	opts := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	if opts.check {
		err = runCheck(ctx, opts, flag.Args())
	} else {
		err = run(ctx, opts)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "erro: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() options {
	defDir := ".synccontacts"
	if d, err := os.UserConfigDir(); err == nil {
		defDir = filepath.Join(d, "synccontacts")
	}
	var o options
	flag.StringVar(&o.credentials, "credentials", "credentials.json", "arquivo do cliente OAuth (tipo \"App para computador\") baixado do Google Cloud")
	flag.StringVar(&o.tokenDir, "token-dir", defDir, "pasta onde ficam salvos os tokens das duas contas")
	flag.BoolVar(&o.dryRun, "dry-run", false, "só mostra o que seria feito, sem alterar nada no destino")
	flag.BoolVar(&o.matchName, "match-name", true, "considera duplicado um contato com o mesmo nome completo")
	flag.BoolVar(&o.labels, "labels", true, "copia os marcadores dos contatos, criando no destino os que faltarem")
	flag.BoolVar(&o.yes, "yes", false, "não pede confirmação antes de criar os contatos")
	flag.BoolVar(&o.relogin, "relogin", false, "descarta os tokens salvos e faz login de novo nas duas contas")
	flag.BoolVar(&o.verbose, "v", false, "lista também os contatos ignorados por serem duplicados")
	flag.IntVar(&o.batchSize, "batch", maxBatchCreate, "quantos contatos criar por requisição (1-200)")
	flag.BoolVar(&o.deleteSource, "delete-source", false, "depois de copiar, APAGA da origem os contatos copiados (agora ou em execuções anteriores)")
	flag.BoolVar(&o.deleteDuplicates, "delete-duplicates", false, "implica -delete-source; apaga também da origem os contatos pulados porque o email ou telefone já existia no destino")
	flag.BoolVar(&o.check, "check", false, "não copia nada: confere no destino o que aconteceu com os contatos dos backups (backup_origem_*.json) e por quê")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Uso: %s [opções]\n       %s -check [opções] [backup_origem_*.json ...]\n\n"+
			"Copia os contatos de uma conta Google (origem) para outra (destino), sem duplicar.\n\n", os.Args[0], os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	o.batchSize = max(1, min(o.batchSize, maxBatchCreate))
	if o.deleteDuplicates {
		o.deleteSource = true
	}
	return o
}

func run(ctx context.Context, o options) error {
	if err := os.MkdirAll(o.tokenDir, 0o700); err != nil {
		return err
	}
	srcScope, srcTokName := people.ContactsReadonlyScope, "token_origem.json"
	if o.deleteSource {
		// Apagar exige permissão de escrita na origem. Usa um token separado
		// para não reaproveitar o token salvo que só tem permissão de leitura.
		srcScope, srcTokName = people.ContactsScope, "token_origem_escrita.json"
	}
	srcTok := filepath.Join(o.tokenDir, srcTokName)
	dstTok := filepath.Join(o.tokenDir, "token_destino.json")
	if o.relogin {
		for _, f := range []string{"token_origem.json", "token_origem_escrita.json", "token_destino.json"} {
			os.Remove(filepath.Join(o.tokenDir, f))
		}
	}

	src, err := authenticate(ctx, o.credentials, srcTok, "ORIGEM", srcScope)
	if err != nil {
		return err
	}
	dst, err := authenticate(ctx, o.credentials, dstTok, "DESTINO", people.ContactsScope)
	if err != nil {
		return err
	}
	if strings.EqualFold(src.Email, dst.Email) {
		return fmt.Errorf("origem e destino são a mesma conta (%s); rode com -relogin e escolha contas diferentes", src.Email)
	}
	fmt.Printf("\nOrigem:  %s\nDestino: %s\n\n", src.Email, dst.Email)

	srcSvc, err := people.NewService(ctx, option.WithHTTPClient(src.Client))
	if err != nil {
		return err
	}
	dstSvc, err := people.NewService(ctx, option.WithHTTPClient(dst.Client))
	if err != nil {
		return err
	}

	srcContacts, err := listContacts(ctx, srcSvc)
	if err != nil {
		return fmt.Errorf("lendo contatos da origem: %w", err)
	}
	dstContacts, err := listContacts(ctx, dstSvc)
	if err != nil {
		return fmt.Errorf("lendo contatos do destino: %w", err)
	}
	fmt.Printf("%d contatos na origem, %d no destino.\n\n", len(srcContacts), len(dstContacts))

	ix := newIndex()
	for _, p := range dstContacts {
		ix.add(p)
	}

	// toDelete: contatos da origem que serão apagados depois da cópia.
	var toCreate, toDelete []*people.Person
	for _, p := range srcContacts {
		kind, reason := ix.match(p, sourceID(src.Email, p), o.matchName)
		if kind == noMatch {
			// Entra no índice para não criar duas vezes contatos que estão
			// duplicados dentro da própria origem.
			ix.add(p)
			toCreate = append(toCreate, p)
			fmt.Printf("  + %s\n", displayName(p))
			continue
		}
		// Contatos que só bateram pelo nome nunca são apagados: podem ser
		// pessoas diferentes, ou ter dados que o contato do destino não tem.
		del := o.deleteSource && (kind == matchImported ||
			(o.deleteDuplicates && (kind == matchEmail || kind == matchPhone)))
		if del {
			toDelete = append(toDelete, p)
			fmt.Printf("  - %s — %s\n", displayName(p), reason)
		} else if o.verbose {
			fmt.Printf("  = %s — %s\n", displayName(p), reason)
		}
	}
	if o.deleteSource {
		toDelete = append(toDelete, toCreate...)
	}

	var groups *groupMapper
	if o.labels {
		if groups, err = newGroupMapper(ctx, srcSvc, dstSvc); err != nil {
			return err
		}
	}
	missingLabels := groups.missing(toCreate)

	fmt.Printf("\nResumo: %d a criar, %d já existem no destino.\n", len(toCreate), len(srcContacts)-len(toCreate))
	if len(missingLabels) > 0 {
		fmt.Printf("Marcadores a criar no destino: %s\n", strings.Join(missingLabels, ", "))
	}
	if o.deleteSource {
		fmt.Printf("%d contatos serão APAGADOS da origem (%s) depois da cópia.\n", len(toDelete), src.Email)
	}
	if len(toCreate) == 0 && len(toDelete) == 0 {
		fmt.Println("Nada a fazer.")
		return nil
	}
	if o.dryRun {
		fmt.Println("Modo -dry-run: nada foi alterado.")
		return nil
	}

	if len(toCreate) > 0 {
		if !o.yes && !confirm(fmt.Sprintf("Criar %d contatos em %s?", len(toCreate), dst.Email)) {
			fmt.Println("Cancelado. Nada foi alterado.")
			return nil
		}
		if err := groups.create(ctx, dstSvc, missingLabels); err != nil {
			return err
		}
		persons := make([]*people.Person, len(toCreate))
		for i, p := range toCreate {
			persons[i] = copyPerson(p, src.Email, groups)
		}
		created, err := batchCreate(ctx, dstSvc, persons, o.batchSize)
		if err != nil {
			// Nada é apagado da origem se a cópia não terminou por completo.
			return fmt.Errorf("%d de %d contatos criados antes da falha; nada foi apagado da origem "+
				"(rodar de novo continua de onde parou): %w", created, len(persons), err)
		}
		fmt.Printf("\n%d contatos copiados para %s.\n", created, dst.Email)
	}

	if len(toDelete) > 0 {
		fmt.Println()
		if !o.yes && !confirm(fmt.Sprintf("APAGAR %d contatos da conta de origem %s?", len(toDelete), src.Email)) {
			fmt.Println("Nenhum contato foi apagado da origem.")
			return nil
		}
		backup, err := writeBackup(toDelete)
		if err != nil {
			return fmt.Errorf("%w; nenhum contato foi apagado", err)
		}
		fmt.Printf("Backup dos contatos que serão apagados: %s\n", backup)
		deleted, err := batchDelete(ctx, srcSvc, toDelete)
		if err != nil {
			return fmt.Errorf("%d de %d contatos apagados da origem antes da falha: %w", deleted, len(toDelete), err)
		}
		fmt.Printf("%d contatos apagados de %s.\n", deleted, src.Email)
	}
	fmt.Println("\nPronto.")
	return nil
}

func confirm(question string) bool {
	fmt.Printf("%s [s/N] ", question)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "s", "sim", "y", "yes":
		return true
	}
	return false
}
