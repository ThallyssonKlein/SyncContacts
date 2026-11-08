# SyncContacts

Copia os contatos do Google Contacts de uma conta Google (**origem**) para
outra (**destino**) sem criar duplicatas. A conta de origem é acessada só em
modo leitura, a não ser que você peça para apagar dela o que foi copiado
(`-delete-source`).

## Como evita duplicatas

Um contato da origem **não** é criado no destino quando:

1. ele já foi importado por este programa antes (cada contato criado guarda o
   id do contato de origem em `clientData`). Então dá para rodar quantas vezes
   quiser.
2. algum **email** dele já existe no destino (sem diferenciar maiúsculas);
3. algum **telefone** dele já existe no destino. A comparação usa os últimos 8
   dígitos, então `+55 (11) 98765-4321`, `011 98765 4321` e `8765-4321` são
   tratados como o mesmo número;
4. o **nome completo** é igual (sem diferenciar acentos/maiúsculas). Para
   desligar essa regra, use `-match-name=false`.

Contatos repetidos dentro da própria origem também são criados uma vez só.

## O que é copiado

Nome, apelidos, emails, telefones, endereços, empresa/cargo, aniversário,
notas, sites, parentes, datas, campos personalizados, mensageiros, profissão e
marcadores (labels). Os marcadores que não existem no destino são criados
(`-labels=false` desliga isso).

**Não** são copiados: fotos, e contatos de "Outros contatos" (os que o Gmail
salva sozinho).

## Configuração (uma vez só)

1. Acesse o [Google Cloud Console](https://console.cloud.google.com/) e crie
   um projeto (ou use um que já tenha).
2. Em **APIs e serviços → Biblioteca**, ative a **Google People API**.
3. Em **APIs e serviços → Tela de permissão OAuth** (Google Auth Platform):
   - tipo de usuário **Externo**;
   - em **Público-alvo → Usuários de teste**, adicione **as duas contas**
     (origem e destino).
4. Em **Credenciais → Criar credenciais → ID do cliente OAuth**, escolha o tipo
   **App para computador** e baixe o JSON.
5. Salve o arquivo como `credentials.json` na pasta do projeto.

> Com o app em modo "Teste", o Google expira os tokens em 7 dias. Se der erro
> de autenticação depois disso, o programa pede o login de novo sozinho.

## Uso

```sh
go build -o synccontacts .

# 1) Simulação: mostra o que seria criado, sem alterar nada
./synccontacts -dry-run

# 2) Cópia de verdade (pede confirmação antes)
./synccontacts
```

Na primeira execução o navegador abre duas vezes: primeiro faça login na conta
de **ORIGEM**, depois na de **DESTINO**. Os tokens ficam em
`~/.config/synccontacts/` e as próximas execuções não pedem login. O programa
mostra os emails das duas contas antes de qualquer alteração e se recusa a
rodar se forem a mesma.

| Opção | Descrição |
|---|---|
| `-dry-run` | só mostra o que seria feito |
| `-v` | lista também os contatos ignorados e o motivo |
| `-match-name=false` | não considera duplicado só por ter o mesmo nome |
| `-labels=false` | não copia marcadores |
| `-yes` | não pede confirmação |
| `-relogin` | esquece os tokens salvos e faz login de novo (para trocar de conta) |
| `-credentials ARQ` | caminho do JSON do cliente OAuth (padrão `credentials.json`) |
| `-token-dir DIR` | onde guardar os tokens |
| `-batch N` | contatos por requisição (1–200, padrão 200) |
| `-delete-source` | depois de copiar, apaga da origem os contatos copiados |
| `-delete-duplicates` | também apaga da origem os que já existiam no destino (por email/telefone) |

Se a cópia falhar no meio (queda de rede, limite da API), é só rodar de novo:
os contatos que já foram criados são reconhecidos e pulados.

## Apagar da origem o que foi copiado (mover em vez de copiar)

```sh
./synccontacts -delete-source -dry-run   # confira a lista marcada com "-"
./synccontacts -delete-source
```

Com `-delete-source`, depois da cópia o programa apaga da origem:

- os contatos criados no destino nesta execução;
- os contatos que já tinham sido copiados por ele em execuções anteriores.

Com `-delete-duplicates`, ele apaga também os contatos que foram pulados porque
o **email ou telefone** já existia no destino. Cuidado: o contato do destino
pode não ter todos os dados do contato da origem.

Travas de segurança:

- contatos que só bateram pelo **nome** nunca são apagados;
- se a cópia falhar no meio, **nada** é apagado;
- pede uma confirmação separada antes de apagar (a não ser com `-yes`);
- antes de apagar, salva os contatos em `backup_origem_<data>.json` na pasta
  atual;
- o Google Contacts também permite reverter alterações dos últimos 30 dias em
  **Configurações → Desfazer alterações**, na conta de origem.

Para apagar, a conta de origem precisa de permissão de escrita. Por isso, na
primeira vez com `-delete-source`, o login da origem é pedido de novo, agora
pedindo acesso de edição.

## Conferir o que foi parar no destino

```sh
./synccontacts -check                     # usa os backup_origem_*.json da pasta atual
./synccontacts -check -v backup_origem_20261108-105808.json
```

Lê os contatos dos backups (os que foram apagados da origem), lista os
contatos do destino e mostra o que aconteceu com cada um:

- **copiado pelo programa**: o contato do destino tem a marca de origem dele;
- **email/telefone já existia**: não foi criado porque já havia um contato com
  esse email ou telefone no destino;
- **só bateu pelo nome**: no destino só existe alguém com o mesmo nome;
- **não está no destino**: nada corresponde a ele (provavelmente foi apagado
  ou editado no destino depois da cópia). Os dados estão no backup.

Também avisa quando o contato do destino não tem algum dado que o da origem
tinha (outro telefone, email, endereço, aniversário, notas...). Por padrão só
lista os casos com problema; com `-v`, lista todos. Só faz login no destino e
não altera nada. As opções vêm antes dos arquivos.

## Testes

```sh
go test ./...
```
